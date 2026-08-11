# theme/

Your theme. The compose stack mounts this directory into the server and sets
`TEMPLATE_DIR=/theme/templates` and `STATIC_DIR=/theme/static`, with
`THEME_RELOAD=true` — so a file dropped in here takes effect on the next page
refresh, with no restart and no rebuild.

- `templates/` — `*.html` (and the two `*.txt` email bodies). A file whose name
  matches an embedded default replaces it; the rest keep falling back. Overriding
  is per *template name*, not per file, so a file defining `{{define
  "products_list"}}` replaces the catalog listing everywhere it appears.
- `static/` — assets by name. A `styles.css` here shadows the bundled one; a
  `logo.svg` here rebrands the header. New names are served too, so an overridden
  template can reference its own `hero.png`.

Both directories start empty, which means the store looks exactly as it does with
no theme at all. Copy a default out of
[`internal/handler/templates/`](../internal/handler/templates) or
[`internal/handler/static/`](../internal/handler/static) and edit the copy.

Nothing in here is used unless `TEMPLATE_DIR` / `STATIC_DIR` point at it, so this
directory is safe to leave empty, delete the contents of, or keep in a branch of
your own. See the README's [Theming](../README.md#theming) section.
