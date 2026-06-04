# Image Loading Performance Pass

## What changed

Every place in the iOS app that renders a remote image now goes through a
single shared `ImagePipeline` (Sources/Services/ImagePipeline.swift).
`RemoteImage` is now a thin SwiftUI view over that pipeline; `AsyncImage`
call sites that fetched remote URLs were rewritten to `RemoteImage`.

### Cache layers (fastest → slowest)
1. **NSCache (in-memory)** — 100 MB / 256 items, keyed by `url|maxPixelSize`.
   Stores fully-decoded `UIImage`s ready to render on the next frame.
2. **On-disk thumbnail cache** — `Caches/ImagePipeline/<sha256(key)>`.
   Stores the downsampled JPEG bytes, so a cold launch reads <300 KB per
   thumbnail instead of re-downloading the full-res photo.
3. **URLSession URLCache** — 50 MB mem / 100 MB disk, honors HTTP
   `Cache-Control`, `ETag`, and 304 Not Modified automatically.
4. **Network**.

### Pipeline guarantees
- **Request coalescing**: 20 chat bubbles for the same image fire one HTTP
  round-trip.
- **Off-main decoding**: ImageIO + `CGImageSourceCreateThumbnailAtIndex`
  runs on a `Task.detached(priority: .userInitiated)` so the main thread
  never blocks on JPEG decode.
- **Downsampling**: decoded to the longest-edge pixel cap the call site
  asked for (`maxPixelSize`). A 44 pt avatar no longer holds a 4 MP buffer.
- **Cancellation**: `RemoteImage` cancels its load in `onDisappear` so
  scrolling past a row tears down its download.
- **Fade-in**: 180 ms ease-in-out on first paint; instant on cache hit.
- **Instrumentation**: `os_signpost` regions `ImageDownload` and
  `ImageDecode` are visible in Instruments → **Points of Interest**.
  `Logger(subsystem: "com.drivebai", category: "ImagePipeline")` prints
  `MEM hit / DISK hit / COALESCE / NET ok / NET HTTP <code>` per request.

## Files touched

| File | Change |
|---|---|
| `Sources/Services/ImagePipeline.swift` (NEW) | Actor-backed pipeline + public facade |
| `Sources/Views/Components/RemoteImage.swift` | Rewritten on top of ImagePipeline; same API, adds `maxPixelSize` |
| `Sources/Views/Chat/Components/ChatBubbleView.swift` | Bubble image hint: `maxPixelSize: 700` |
| `Sources/Views/Chat/Components/ChatRowView.swift` | Row thumbnail hint: `maxPixelSize: 200` |
| `Sources/Views/Chat/ChatDetailsView.swift` | Car cover 240, avatar 180, media grid 320 |
| `Sources/Views/Chat/CounterpartyProfileView.swift` | Avatar: `maxPixelSize: 400` |
| `Sources/Views/Chat/Components/DriverDocumentsSection.swift` | Migrated to `AttachmentDownloadService` (Bearer auth + cache-hit short-circuit) |
| `Sources/Views/Main/ProfileView.swift` | `AsyncImage` → `RemoteImage` (300px) |
| `Sources/Views/Main/DiscoverMapView.swift` | Map pin image: 500px |
| `Sources/Views/MyCars/OwnerMyCarsView.swift` | `AsyncImage` → `RemoteImage` (300px) |
| `Sources/Views/MyCars/CreateListingFlowView.swift` | `AsyncImage` → `RemoteImage` (800px) |
| `Sources/Views/MyCars/CarPhotosEditView.swift` | `AsyncImage` → `RemoteImage` (800px) |
| `Sources/Views/Accidents/AccidentReportView.swift` | Accident thumbnail: `AsyncImage` → `RemoteImage` (300px) |

`AttachmentDownloadService` (PDF/file viewer) was not changed — it already
short-circuits on cache hit and adds Bearer auth.

## Screens fixed

| Screen | Before | After |
|---|---|---|
| Chat with N image attachments | Each bubble independently runs `URLSession.shared.data(from:)`, decodes on the SwiftUI render thread, no shared cache between bubbles or chats | Placeholder visible on the first frame; same-URL bubbles share one in-flight task; thumbnails decoded off-main; cache hit makes re-entry instant |
| Driver Documents section (Requests tab) | Each row spun its own `URLSession.shared.data(from:)` and wrote to a per-row cache file — second tap re-downloaded | Tap routes through `AttachmentDownloadService` — second tap is a `FileManager.fileExists` check; Bearer auth respected |
| Chats list (`ChatRowView`) | 50×50 thumbnails decoded full-res in memory | Decoded to 200px thumbnails; ~80% less memory per row |
| Chat details | Car cover, counterparty avatar, shared media each fetched independently | Single pipeline; tiny avatars no longer hold 4 MP bitmaps |
| Discover (`DiscoverView`, `DiscoverMapView`) | `RemoteImage` used `URLSession.shared.data(from:)` per render and didn't cache between scroll cycles | NSCache + disk thumbnails — re-scrolling is instant |
| Today listings (`ListingCard`) | Same | Same fix |
| My Cars (`OwnerMyCarsView`, `CreateListingFlowView`, `CarPhotosEditView`) | `AsyncImage` — silent failures, no shared cache | `RemoteImage` w/ pipeline cache; loads visible after first run |
| Profile (`ProfileView`) | `AsyncImage` | `RemoteImage` w/ 300px thumbnail |
| Accident report wizard (attachment thumbnails) | `AsyncImage` | `RemoteImage` w/ 300px thumbnail |

## How to validate

### Smoke test (manual)
1. Cold-launch the app and open a chat with several image attachments.
2. Placeholders appear immediately; images fade in over ~200ms.
3. Scroll up and back down: thumbnails reappear instantly (memory cache hit).
4. Kill the app and reopen the same chat: thumbnails appear within ~50ms
   even on poor connectivity (disk cache hit).
5. Open Requests → Driver Documents → tap a document. Tap "Done" then tap
   again: second tap is instant.
6. Enable airplane mode. Reopen any previously-viewed chat. Thumbnails
   render from disk cache. Brand-new images show the error tile.

### Console.app log evidence
Subsystem `com.drivebai`, category `ImagePipeline`:
```
MEM hit: https://drivebai-api-team.fly.dev/uploads/chats/.../foo.jpg @700px
DISK hit: https://drivebai-api-team.fly.dev/uploads/chats/.../bar.jpg @700px
COALESCE: https://drivebai-api-team.fly.dev/uploads/chats/.../baz.jpg @700px
NET ok: https://drivebai-api-team.fly.dev/uploads/chats/.../qux.jpg (124583B)
```
You should see a mix of `MEM hit` + `DISK hit` after the first view, and at
most one `NET ok` per (URL, pixel size) combo per cold launch.

### Instruments
Run an **Instruments → Points of Interest** trace. Look for:
- `ImageDownload` intervals — should be far fewer than the number of visible
  thumbnails (one per unique URL).
- `ImageDecode` intervals — should run on a non-main queue and last <50ms
  per thumbnail.

## Caveats / known-correct trade-offs
- **Thumbnails are re-encoded as JPEG q0.9** for the disk cache. The
  original file is untouched on the server; tap-to-open still fetches the
  full-resolution file via `AttachmentDownloadService`.
- **`maxPixelSize` is a hint, not a contract**. Pass a number close to the
  actual pixel footprint (display points × device scale). Default `1024`
  is reasonable but wasteful for small thumbnails — please pass `200-300`
  for 44–60 pt avatars and small thumbnails.
- **Local-data paths (`UIImage(data:)`)** for in-flight uploads are
  untouched — they aren't network-bound and don't benefit from the pipeline.

## Future improvements (not in this pass)
- HEIC encoder for the disk cache (smaller files, better quality at same
  size). Currently uses JPEG q0.9 because every iOS device has a hardware
  JPEG decoder.
- Background prefetch when the user opens the chat list — `ImagePipeline.prefetch(urls:)`
  helper already exists; wiring it into `ChatsListViewModel` is a one-line
  follow-up.
- Memory-pressure listener to halve `NSCache.totalCostLimit` on
  `UIApplication.didReceiveMemoryWarningNotification`.
