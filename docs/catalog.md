# Catalog

A **product** is a catalog entry; a **variant** is what a customer actually buys, and
carries the price and the stock count. A product with no options — a single-edition book —
still has exactly one variant, with every option left blank. That is deliberate: cart,
order and stock code then never branches on "has options versus not".

A product is either **physical** or a **digital download**, set by its *kind*. A download
has no stock, needs no delivery address, and is delivered by a per-buyer link rather than a
parcel — see [Digital downloads](#digital-downloads).

Prices are **integer cents** everywhere in the code and the database. The decimal point
exists only in forms and rendered pages, because a float total rounded differently from a
payment gateway's amount string is a real and hard-to-find class of bug.

Manage the catalog at `/admin/products`.

## Variant options

A variant is told apart from its siblings by up to **three options, named per product**. A
t-shirt declares `Size` and `Colour`; a book declares `Cover`; a conference recording
declares `Format`. The names live on the product and the values on each variant, so the
storefront's selector is headed in the shop's own words rather than in ours.

Names fill in order, and two slots may not share a name. Both are refused on the form
rather than by a constraint, so the message lands on the field.

**The names are not attached to the category, deliberately.** Categories here are
many-to-many precisely so that a book can also be a gift — so option structure hanging off
them would give that product two contradictory answers, and adding a "Sale" category for a
promotion would change what a variant *is*. Shopify and WooCommerce declare options per
product exactly like this; Magento, Saleor and Sylius add a reusable template on top and
every one of them keeps that template separate from the browsing taxonomy. A template layer
here would be strictly additive later, because the values live in these columns either way.

## Categories

**A category is a row, not a string on the product.** Two tables: `categories`, and a
`product_categories` join.

| Column | Is |
|---|---|
| `slug` | The public parameter — `/products?category=books`. Unique |
| `name` | What a shopper reads |
| `position` | The display order. Sorting by `name` would put "Apparel" ahead of "Books" for ever, and a shop owner wants their own order |

**A product may be in several categories**, which is why there is a join table rather than a
column. A book that is also a gift belongs in both, and making a shop owner choose is a
decision the store has no business making for them. The cost is one extra query wherever
categories are read — paid in the admin, and deliberately not on the storefront cards, which
do not show them.

**Deleting a category unlinks its products; it never deletes them.** The cascade is on the
join table alone. This is the same stance as refusing to delete a product an order references:
a taxonomy edit must not be able to remove things people bought. The cost is that deleting an
unused category looks like it did nothing, so the admin says how many links it removed.

Manage them at `/admin/categories`.

## Product images

**A product image is always bytes this store holds.** There is no way to point a product at
a URL on somebody else's server: those bytes can change or vanish without warning, and a
product page with a broken picture is worse than one with none. The admin has no URL field,
and hand-crafting the parameter does nothing — `UpdateProduct` does not write either image
column.

Two backends, mutually exclusive, both behind `blob.Storage`:

| | Set | Image URL | Suits |
|---|---|---|---|
| **Object storage** | `BLOB_*` | the bucket's public hostname | anything scaled out; R2, GCS interop, MinIO |
| **A local directory** | `IMAGE_DIR` | `/images/...`, served by this server | one instance with a persistent volume |
| **Neither** | — | products have no images, and the admin says so | a catalog that does not need pictures |

`IMAGE_DIR` is the simpler shape: one binary, one directory, no object storage to run. Its
limitation is worth stating plainly because it is the thing that will bite — **two instances
do not share a directory.** Behind a load balancer, or on a platform that scales to zero and
restarts elsewhere, an image uploaded by one instance is a 404 from the other. Use a bucket
there.

With a bucket, **images are served straight from it and never proxied through the store**,
so the bucket must be publicly readable at `BLOB_PUBLIC_BASE_URL` and a CDN in front of it
does the work. With `IMAGE_DIR` the server serves them itself, from a same-origin path — which
is why `img-src` can be `'self'` with no external origin allowed at all.

**`products.image_key` is the only thing stored** — the URL is computed when a page is
rendered, by resolving the key against whichever backend is running. That is what makes the
same row work on a development machine serving from a directory and in production serving
from R2: **switching backends needs no data migration.** Storing the URL as well would bake
one deployment's answer into every row.

A product with no image gets a bundled placeholder rather than a gap, so a catalog of mixed
products keeps its shape while photographs are still being taken.

Two consequences worth knowing:

- **Uploads are validated on their sniffed magic bytes**, not the filename and not the
  browser's `Content-Type`, and the stored extension comes from the sniffed type too. A
  publicly readable bucket that will serve `evil.html` because somebody named their upload
  that is a cross-site scripting hole on a hostname you own — and the same is true of a
  directory this server serves. JPEG, PNG, GIF and WebP; 5 MB.
- **Replacing an image writes a new key**, so the new photograph is visible immediately.
  A stable key would need a CDN purge on every replacement — an operation this store has
  no credentials for and no way to verify — and until it happened the old picture would
  keep being served.

With a bucket, `BLOB_ENDPOINT` and `BLOB_PUBLIC_BASE_URL` are separate values because the
address a bucket is *written* through and the address it is *read* from are routinely
different:
R2 writes to `<account>.r2.cloudflarestorage.com` and reads from a custom domain, and in
the compose stack the server writes to `minio:9000` while your browser reads from
`localhost:9000`. Only the operator knows the second one, so it is not derived.

**Why minio-go and not the AWS SDK.** Since `aws-sdk-go-v2/service/s3` v1.73.0 every
`PutObject` carries a CRC32 checksum by default, which
[broke R2, GCS interop and older MinIO](https://github.com/aws/aws-sdk-go-v2/discussions/2960) —
the three stores this targets — while being correct for the one it is least likely to be
pointed at. minio-go speaks the conservative subset all of them agree on.

The upload order is chosen so no failure leaves a product pointing at nothing: store the
new object, point the product at it, and only then delete the old one. A failure part-way
leaves the previous image working, or at worst an orphaned object that costs a few
kilobytes and is logged.

**How images load** is the browser's own lazy loading and nothing else — no
`IntersectionObserver`, no JavaScript, no CSP directive involved. The catalog grid marks
everything after the first row `loading="lazy"`; the first four cards are left eager, because
lazy-loading an image that is already on screen defers exactly the picture that decides how
fast the page *feels* loaded. Four is a deliberate guess: the grid asks for as many columns as
fit, so the real count is a CSS decision the server cannot see, and four over-fetches slightly
on a phone and under-fetches on a wide monitor. A product page's own photograph is eager and
`fetchpriority="high"`, being the one image that page is about.

There are **no `width` and `height` attributes**, and that is not an oversight. The fixed-ratio
frame (`aspect-ratio: 4 / 5`) already reserves the space before any bytes arrive, so there is
no layout shift left to prevent — and the store never records a photograph's real dimensions,
so any numbers put there would be a guess about an image that is going to be cropped to the
frame anyway.

## Digital downloads

A product whose kind is **digital** is delivered as files rather than as a parcel. It has no
stock, needs no delivery address, and each buyer gets a link that can be withdrawn without
touching anybody else's.

**Files belong to the product; variants say which files they grant.** A conference recording
sold as an audio set and a video set is one product, two variants, and a tick list saying
which files each includes — so an "Audio + Video" bundle costs a row rather than a second
upload of the same two gigabytes.

**Private storage, always.** `DOWNLOAD_DIR` or the `DOWNLOAD_*` bucket, configured separately
from images and never the same place. The image bucket is anonymously readable by design —
that is what lets a CDN serve product photographs — so a purchased file in it would be one URL
guess away from everybody, and public access on GCS and R2 is a whole-bucket toggle rather
than something a prefix can carry. The server **refuses to boot** if the download directory
overlaps `IMAGE_DIR`, or if the download bucket is the image bucket.

### How a buyer gets their file

```
paid order
  └─ entitlement per digital line, with a 32-byte token
       └─ emailed as  {BASE_URL}/downloads/{token}

GET /downloads/{token}            the files this entitlement grants
GET /downloads/{token}/{fileID}   check it is not revoked, check the file
                                  belongs to this variant, record the download,
                                  then 302 to a signed URL that expires in five
                                  minutes — or stream, on the disk backend
```

The token in the URL is the whole credential: there is no account and no login. **Only its
SHA-256 hash is stored**, so a dump of the entitlements table is a list of hashes rather than
a set of working links — and the consequence, stated plainly, is that a confirmation email
that never arrives cannot be recovered from. Issue a fresh entitlement in that case.

The link never points at the bucket. Authorising and recording happen before any bytes move,
which is what makes revocation take effect on the next click and makes the counts trustworthy.
The signed URL is minted per click, so one forwarded to a friend is already expired.

A presigned URL's signature covers the `Host` header, so it must be signed for the address the
*browser* will use rather than the one the server connects through. Those are the same
everywhere except a container stack, which is what `DOWNLOAD_PUBLIC_ENDPOINT` is for — compose
sets it, because the server reaches MinIO at `minio:9000` and a browser reaches it at
`localhost:9000`.

### Revoking

`/admin/orders/{id}` lists an order's downloads with a **Revoke** button and how many times
each has been taken. Revoking stops that buyer and nobody else, takes effect on their next
click, and is reversible. These are the only forms on the order page — everything else there
is read-only, because an order records what happened and a button that changed it would be a
way to record money that never arrived. Revoking changes no financial fact.

### Statistics

`/admin/products/{id}/downloads` reports total downloads, how many distinct buyers took
something, per-file counts, and the most recent downloads with the buyer against each.

**A count is an authorised click, not a completed transfer**, and the page says so. With a
signed URL the bytes never come through the store, so it cannot know how a transfer ended; a
connection that dropped at 80% and was started again is two.

Asking the bucket instead does not work, and not for a reason that might be fixed later:
neither GCS nor R2 exposes per-object read counts, and a presigned URL is *anonymous* to the
bucket — it has no idea which buyer, order or entitlement. That mapping exists only here.

### Uploads

Through the server, streamed to storage. The request is spooled to a temporary file and
streamed on from there, so memory does not track the file size — measured, a 477 MB upload
grew the server by 75 MB and a 1.43 GB upload by 65 MB. What it does cost is temporary disk
(the file exists twice for the length of the request) and a held-open connection.

Uploading straight from the browser to the bucket would avoid both, and is deliberately not
built: it needs a CORS policy on the bucket, a widened `connect-src`, a JavaScript uploader
and an orphan sweep, and uploads here are rare and done by one operator watching them.

`DOWNLOAD_MAX_BYTES` caps one file, default 2 GiB. Unlike images there is no allow-list of
types: the bucket is private, every read is authorised, and the response carries the stored
`Content-Type`, `nosniff` and an attachment disposition — so refusing an unusual format would
only stop a shop selling what it sells.

### Changing a product's kind

Frozen once the product has been **ordered**, and while a digital product still has **files
attached**. Neither protects purchase history — `order_items` snapshots the kind, so a
completed sale is already safe. What they protect is live state: flipping to digital would
leave a stock count nothing decrements, and flipping the other way would leave objects in
storage with nothing listing them. The second is a step rather than a dead end: remove the
files and the kind becomes changeable.

## Seeding

`cmd/seed` loads a plain products JSON file:

```json
[
  {
    "categories": ["books"],
    "slug": "the-quiet-machine",
    "title": "The Quiet Machine",
    "description": "Paperback, 248 pages.",
    "active": true,
    "option1_name": "Cover",
    "variants": [
      { "sku": "BOOK-TQM-PB", "option1": "Paperback", "price_cents": 24900, "stock_qty": 12, "active": true },
      { "sku": "BOOK-TQM-HC", "option1": "Hardcover", "price_cents": 34900, "stock_qty": 3, "active": true }
    ]
  }
]
```

`option1_name` through `option3_name` name the product's variant options; `option1` through
`option3` are each variant's values for them. Omit them all for a product with only one
version of itself.

### Seeding a digital product

`kind: "digital"` plus a `files` list, which is the one thing a fixture can say that the
domain type deliberately cannot — an object key is storage's to choose, and a size and a
content type are facts about bytes:

```json
{
  "slug": "a-quiet-hour",
  "kind": "digital",
  "option1_name": "Format",
  "variants": [
    { "sku": "QH-AUDIO",            "option1": "Audio",              "price_cents": 8000 },
    { "sku": "QH-AUDIO-TRANSCRIPT", "option1": "Audio + transcript", "price_cents": 12000 }
  ],
  "files": [
    { "path": "downloads/sample-recording.wav",  "title": "A Quiet Hour — recording",
      "variants": ["QH-AUDIO", "QH-AUDIO-TRANSCRIPT"] },
    { "path": "downloads/sample-transcript.pdf", "title": "Transcript",
      "variants": ["QH-AUDIO-TRANSCRIPT"] }
  ]
}
```

`path` is **relative to the seed file's own directory**, and anything absolute or climbing
out of it is refused — a seed file is data, and data that can name any path on the machine
and have it uploaded to a bucket is a way to exfiltrate a private key by editing JSON.
`variants` are SKUs, the same natural key the variants themselves match on. `title` defaults
to the filename. The content type is **sniffed**, not taken from the extension.

Seeding files needs somewhere private to put them — `DOWNLOAD_DIR` or the `DOWNLOAD_*`
bucket. Nothing else the server requires is needed to seed. A fixture with files and no
storage configured is refused rather than half-loaded, along with files on a physical
product, a SKU that is not one of that product's variants, and a file that is not there —
all before a single row is written.

**Files match on the name they were seeded from**, so a second run retitles and re-links
rather than uploading a second copy. That is not only tidiness: replacing the row would mint
a new file id, and a buyer holding an entitlement would find that what they paid for had
quietly become something else.

The shipped fixture demonstrates the part that matters — the recording is granted by both
variants and the transcript by only one, which is what a per-variant file list is *for*. Both
sample files in `testdata/downloads/` are real and playable/readable, not renamed
placeholders.

`slug` may be omitted and is then derived from the title. Seeding is rerunnable: products
match on `slug` and variants on `sku`, so a second run updates titles and prices rather
than duplicating rows — and it leaves `stock_qty` on rows that already exist alone, since a
fixture is a starting point and not the truth about inventory. Variants missing from the
file are not deleted.

`categories` is a list of category slugs, and any that do not exist yet are created, named by
title-casing the slug — `gift-cards` becomes "Gift Cards". That keeps a fixture
self-contained — seeding a fresh database needs no prior trip to the admin — at the cost of a
typo becoming a new category rather than an error. A category that already exists is left
exactly as it is, name and position included, so re-seeding never undoes an edit.
Give it a proper name in the admin afterwards — products stay linked through that rename,
because the link is by id. Changing the *slug* is the one to think about, since that is what
filter URLs carry.

**There is no `image_url` field**, and an unknown field is an error rather than being
ignored — so a file carrying one is rejected with the field named. A fixture cannot upload
bytes, so the only thing it could set is a URL to somebody else's server, which is exactly
what is no longer allowed. Re-seeding therefore never disturbs an uploaded image.

```sh
make seed                              # testdata/products.json
make seed SEED_FILE=my-catalog.json
```

`testdata/products.json` is generic sample data: fictional titles, no real contact details.
