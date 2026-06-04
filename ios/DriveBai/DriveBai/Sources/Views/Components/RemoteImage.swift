import SwiftUI
import os

/// A SwiftUI view that renders a remote image via the shared `ImagePipeline`.
///
/// Layout-preserving drop-in for SwiftUI's `AsyncImage`: it takes the size its
/// parent offers, shows a tinted placeholder while loading, fades the final
/// image in when ready, and surfaces the HTTP status / error in DEBUG.
///
/// All caching, downsampling, request coalescing, and prefetching is delegated
/// to `ImagePipeline.shared`. This view is intentionally thin so multiple
/// instances of the same URL can collapse onto a single in-flight task.
///
/// Caller hints:
/// - `contentMode`: same as `Image.aspectRatio` — `.fill` (default) or `.fit`.
/// - `maxPixelSize`: longest-edge pixel cap used for downsampling. Defaults to
///   `1024`, which is right for full-bleed bubbles. For small thumbnails (e.g.
///   44 pt avatars), pass `200` to keep the in-memory bitmap tiny.
struct RemoteImage: View {
    let url: URL
    var contentMode: ContentMode = .fill
    var maxPixelSize: CGFloat = 1024

    @State private var image: UIImage?
    @State private var failureReason: String?

    private static let logger = Logger(subsystem: "com.drivebai", category: "RemoteImage")

    var body: some View {
        ZStack {
            if let image {
                Color.clear
                    .overlay(
                        Image(uiImage: image)
                            .resizable()
                            .aspectRatio(contentMode: contentMode)
                    )
                    .clipped()
                    .transition(.opacity)
            } else if let failureReason {
                Rectangle()
                    .fill(Color.driveBaiPrimary.opacity(0.15))
                    .overlay(
                        VStack(spacing: 4) {
                            Image(systemName: "exclamationmark.triangle")
                                .font(.system(size: 24))
                                .foregroundColor(Color.driveBaiPrimary.opacity(0.5))
                            #if DEBUG
                            Text(failureReason)
                                .font(.system(size: 9))
                                .foregroundColor(.secondary)
                                .lineLimit(2)
                                .padding(.horizontal, 4)
                            #endif
                        }
                    )
            } else {
                Rectangle()
                    .fill(Color.driveBaiPrimary.opacity(0.10))
                    .overlay(ProgressView())
            }
        }
        .task(id: cacheKey) {
            await load()
        }
        .onDisappear {
            Task { await ImagePipeline.cancel(url: url, maxPixelSize: maxPixelSize) }
        }
    }

    /// Identity for SwiftUI's `task(id:)` — when either the URL or the
    /// requested size changes, the previous task is cancelled and a fresh
    /// load is kicked off.
    private var cacheKey: String { "\(url.absoluteString)|\(Int(maxPixelSize))" }

    private func load() async {
        // Don't show a fade-in if we already had the image cached in-process.
        let wasEmpty = (image == nil && failureReason == nil)

        do {
            let img = try await ImagePipeline.load(url: url, maxPixelSize: maxPixelSize)
            await MainActor.run {
                if wasEmpty {
                    withAnimation(.easeInOut(duration: 0.18)) { self.image = img }
                } else {
                    self.image = img
                }
                self.failureReason = nil
            }
        } catch is CancellationError {
            // Normal — view disappeared. Don't show an error.
        } catch {
            Self.logger.error("Image failed \(url.absoluteString): \(error.localizedDescription)")
            await MainActor.run {
                self.failureReason = error.localizedDescription
            }
        }
    }
}
