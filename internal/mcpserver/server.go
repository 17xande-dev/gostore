// Package mcpserver is the store's Model Context Protocol endpoint: the admin,
// for an AI client acting as an administrator.
//
// It is a second door onto the same rooms. Authentication is an API token
// (internal/auth) standing in for a session; authorisation is the same
// auth.Permission each admin route names, checked before every tool runs; and
// the rules for changing anything are internal/admin's, shared with the HTML
// handlers, so neither door can skip one.
//
// Served stateless over Streamable HTTP: every call carries its token and is
// authenticated afresh, there is no MCP session to lose on a restart or split
// between instances, and a revoked token stops working on its next call.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/downloads"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/gostore/internal/outbox"
	"github.com/17xande-dev/gostore/internal/validate"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Deps is everything the tools reach for, by name, for the reason handler.Deps
// gives.
type Deps struct {
	Log     *slog.Logger
	Users   *auth.Store
	Catalog *catalog.Store
	Orders  *orders.Store
	Grants  *downloads.Store
	Outbox  *outbox.Store
	// Images is the public image store, for turning an image key into the URL a
	// client can show. Uploading stays in the admin — see the package tools.
	Images blob.Storage
	// BaseURL is the store's origin, for the admin links tools hand back where a
	// job belongs in the browser (uploading an image, say).
	BaseURL string
	// Version is reported to clients in the MCP handshake.
	Version string
}

// ToolPerm is one registered tool and the permission it needs, recorded as it
// is registered so a test can sweep them — the AdminProtectedRoutes idea.
type ToolPerm struct {
	Name string
	Perm auth.Permission
}

// Server is the MCP server and the record of its tools.
type Server struct {
	d     Deps
	srv   *mcp.Server
	tools []ToolPerm
}

// New builds the server and registers every tool.
func New(d Deps) *Server {
	if d.Images == nil {
		d.Images = blob.Unconfigured{}
	}
	s := &Server{
		d: d,
		srv: mcp.NewServer(&mcp.Implementation{Name: "gostore", Version: d.Version}, &mcp.ServerOptions{
			Instructions: "Tools for administering this gostore shop: its products, variants, " +
				"categories and orders. You act as the administrator whose API token you hold, " +
				"with exactly that account's role. Prices are decimal strings in the store's " +
				"currency, like \"149.99\". To set a product's image, call create_image_upload " +
				"and send the file to the URL it returns over HTTP (e.g. curl -T). Downloadable " +
				"files are uploaded in the web admin, not here.",
		}),
	}
	s.registerTools()
	return s
}

// Tools returns every registered tool and its permission. A copy, so a caller
// cannot edit the record.
func (s *Server) Tools() []ToolPerm {
	return append([]ToolPerm(nil), s.tools...)
}

// Handler is the /mcp endpoint: the bearer-token check in front of the
// Streamable HTTP transport.
func (s *Server) Handler() http.Handler {
	return s.handler(s.verify)
}

// handler is the endpoint behind a given verifier. Separate from Handler only so
// a test can put a token the role check would refuse in front of the tools, to
// prove each tool's own permission check still holds on its own.
func (s *Server) handler(verify mcpauth.TokenVerifier) http.Handler {
	transport := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	// The SDK's middleware, rather than one of ours, because it is the only way
	// the identity reaches a tool handler: it stores what the verifier returns
	// under its own context key and copies it into each request's Extra.
	return mcpauth.RequireBearerToken(verify, nil)(transport)
}

// userKey is where the verified account travels in TokenInfo.Extra.
const userKey = "gostore.user"

// verify turns a bearer token into the account it acts as.
//
// Two rules carried over from the session lookup (see middleware.RequireAdmin):
// every kind of unusable token gets the one answer, and a failure of the store
// is a 500 rather than a 401 — telling a client its token is wrong during a
// database outage sends somebody off to make a new one for nothing. The 500's
// message is ours: the SDK writes a verifier's error text into the response,
// and a database error is not something to hand a caller.
//
// A token whose account's role does not hold auth.PermAPITokens is refused like
// any other unusable one. Demoting an account deletes its tokens, so this is
// for the ones made before only administrators could make them — and for any
// that the deletion somehow missed, which should then fail closed.
func (s *Server) verify(ctx context.Context, token string, r *http.Request) (*mcpauth.TokenInfo, error) {
	info, err := s.lookup(ctx, token, r)
	if err != nil {
		return nil, err
	}
	if user := info.Extra[userKey].(auth.User); !user.Can(auth.PermAPITokens) {
		s.d.Log.Warn("mcp: refused a token whose role may not hold one", "user", user.ID, "role", user.Role)
		return nil, mcpauth.ErrInvalidToken
	}
	return info, nil
}

// lookup is verify without the role check: the token, its account, and the
// bookkeeping.
func (s *Server) lookup(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	rec, user, err := s.d.Users.APITokenUser(ctx, token)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return nil, mcpauth.ErrInvalidToken
	case err != nil:
		s.d.Log.Error("mcp: cannot verify an api token", "error", err)
		return nil, errors.New("internal server error")
	}
	if err := s.d.Users.TouchAPIToken(ctx, rec.ID); err != nil {
		// Bookkeeping; the call is authenticated either way.
		s.d.Log.Warn("mcp: could not record api token use", "token", rec.ID, "error", err)
	}
	user.PasswordHash = ""
	return &mcpauth.TokenInfo{
		UserID:     user.ID,
		Expiration: rec.ExpiresAt,
		Extra:      map[string]any{userKey: user, "token": rec},
	}, nil
}

// callerOf reads the verified account back out of a tool request.
func callerOf(req *mcp.CallToolRequest) (auth.User, auth.APIToken, bool) {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return auth.User{}, auth.APIToken{}, false
	}
	user, ok := req.Extra.TokenInfo.Extra[userKey].(auth.User)
	rec, _ := req.Extra.TokenInfo.Extra["token"].(auth.APIToken)
	return user, rec, ok
}

// toolFunc is a tool's own logic, given the caller already authorised.
type toolFunc[In, Out any] func(ctx context.Context, caller auth.User, in In) (Out, error)

// add registers a tool behind a permission. The permission is a required
// argument, as it is for an admin route, so a new tool has to say what it is
// for; auth.PermRead is how it says "any administrator".
func add[In, Out any](s *Server, perm auth.Permission, t *mcp.Tool, fn toolFunc[In, Out]) {
	s.tools = append(s.tools, ToolPerm{Name: t.Name, Perm: perm})
	mcp.AddTool(s.srv, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		caller, rec, ok := callerOf(req)
		if !ok {
			// Unreachable behind Handler; reachable if the server is ever mounted
			// without it, which must fail closed.
			return nil, zero, errors.New("not authenticated")
		}
		if !caller.Can(perm) {
			s.d.Log.Warn("mcp: tool refused for role", "tool", t.Name, "user", caller.ID, "role", caller.Role, "needs", perm)
			return nil, zero, fmt.Errorf("your role (%s) cannot do this: it needs %s", caller.Role.Label(), perm)
		}
		out, err := fn(withToken(ctx, rec), caller, in)
		if err != nil {
			return nil, zero, s.toolError(t.Name, caller, err)
		}
		if perm != auth.PermRead {
			// Writes are logged at info, as the HTML admin's deliberate acts are,
			// with the token so "which assistant did this" has an answer.
			s.d.Log.Info("mcp: tool call", "tool", t.Name, "user", caller.ID, "token", rec.ID)
		}
		return nil, out, nil
	})
}

// userError is a refusal worth showing the caller as it stands: a validation
// message, a missing record, a rule the store enforces.
type userError struct{ msg string }

func (e userError) Error() string { return e.msg }

func refuse(format string, args ...any) error { return userError{fmt.Sprintf(format, args...)} }

// fieldErrors is a refusal made of form field errors, phrased for a caller that
// sees no form: "field: message; field: message".
func fieldErrors(errs validate.FormErrors) error {
	return userError{errs.String()}
}

// toolError decides what a failed tool call tells its caller. Refusals and
// missing records are said plainly; anything else is logged in full and
// reported without detail, as a 500 page would be.
func (s *Server) toolError(tool string, caller auth.User, err error) error {
	var ue userError
	switch {
	case errors.As(err, &ue):
		return ue
	case errors.Is(err, catalog.ErrNotFound), errors.Is(err, orders.ErrNotFound), errors.Is(err, downloads.ErrNotFound):
		return errors.New("not found")
	}
	s.d.Log.Error("mcp: tool failed", "tool", tool, "user", caller.ID, "error", err)
	return errors.New("internal error; the store's log has the details")
}
