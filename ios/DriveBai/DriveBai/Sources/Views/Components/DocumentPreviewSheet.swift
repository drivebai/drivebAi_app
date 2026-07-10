import SwiftUI
import QuickLook
import UIKit

// MARK: - Document Preview Sheet
//
// QuickLook-based preview for uploaded/staged documents (QA pts 8 & 11).
// Present it as a `.sheet`. Two sources:
//   - `.localData` — bytes already in memory (a staged, not-yet-uploaded
//     doc). Images take a fast path to a zoomable `Image`; everything else
//     (PDFs, unknown types) is written to a tmp file and handed to
//     QuickLook.
//   - `.remoteURL` — a signed URL string from the backend (relative or
//     absolute; resolved through `ImageURLHelper`). Downloaded via the
//     existing `AttachmentDownloadService` (caches dir, Bearer token,
//     stable filenames) with a spinner while fetching, then previewed.

struct DocumentPreviewSheet: View {
    enum Source {
        case localData(Data, filename: String, mimeType: String?)
        case remoteURL(String, filename: String)
    }

    let source: Source

    @Environment(\.dismiss) private var dismiss
    @State private var phase: Phase = .loading

    private enum Phase {
        case loading
        case image(UIImage)
        case file(URL)
        case failed(String)
    }

    var body: some View {
        NavigationStack {
            content
                .navigationTitle(displayFilename)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    // Share / Save to Files for a downloaded (or staged)
                    // document once it's on disk. The iOS share sheet's
                    // destinations include "Save to Files".
                    if case .file(let url) = phase {
                        ToolbarItem(placement: .navigationBarLeading) {
                            ShareLink(item: url) {
                                Image(systemName: "square.and.arrow.up")
                            }
                        }
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { dismiss() }
                    }
                }
        }
        .task { await load() }
    }

    @ViewBuilder
    private var content: some View {
        switch phase {
        case .loading:
            VStack(spacing: 12) {
                ProgressView()
                Text("Loading document…")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        case .image(let image):
            ZoomableImageView(image: image)
                .ignoresSafeArea(edges: .bottom)
        case .file(let url):
            QuickLookPreview(url: url)
                .ignoresSafeArea(edges: .bottom)
        case .failed(let message):
            VStack(spacing: 12) {
                Image(systemName: "exclamationmark.triangle")
                    .font(.system(size: 36))
                    .foregroundColor(.secondary)
                Text("Couldn't load document")
                    .font(.headline)
                Text(message)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
            }
            .padding()
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private var displayFilename: String {
        switch source {
        case .localData(_, let filename, _): return filename
        case .remoteURL(_, let filename): return filename
        }
    }

    // MARK: - Loading

    private func load() async {
        switch source {
        case .localData(let data, let filename, let mimeType):
            // Image fast-path: local bytes + image mime → zoomable Image.
            let isImageMime = mimeType?.hasPrefix("image/") ?? false
            if isImageMime, let image = UIImage(data: data) {
                phase = .image(image)
                return
            }
            // Fall back to sniffing the bytes even without a mime tag.
            if mimeType == nil, let image = UIImage(data: data) {
                phase = .image(image)
                return
            }
            do {
                let url = try Self.writeToTemp(data: data, filename: filename, mimeType: mimeType)
                phase = .file(url)
            } catch {
                phase = .failed(error.localizedDescription)
            }

        case .remoteURL(let urlString, let filename):
            guard let url = ImageURLHelper.fullURL(for: urlString) else {
                phase = .failed("The document URL is invalid.")
                return
            }
            do {
                // Cache key = the server's stored-file name (UUID-suffixed,
                // e.g. "registration_ab12….pdf"), NOT the user-facing display
                // filename. Display names are type-based and collide across
                // cars ("registration.pdf" everywhere), and the download
                // service dedupes by filename — keying on the display name
                // made the cache serve car A's registration when previewing
                // car B's. URL.lastPathComponent excludes the ?sig=&exp=
                // query, so the key stays stable across re-signed URLs and
                // the cache still hits on re-open.
                let cacheKey = url.lastPathComponent.isEmpty ? filename : url.lastPathComponent
                let local = try await AttachmentDownloadService.shared.download(
                    url: url,
                    suggestedFilename: cacheKey
                )
                if let data = try? Data(contentsOf: local), let image = UIImage(data: data),
                   ["jpg", "jpeg", "png", "heic"].contains(local.pathExtension.lowercased()) {
                    phase = .image(image)
                } else {
                    phase = .file(local)
                }
            } catch {
                phase = .failed(error.localizedDescription)
            }
        }
    }

    /// Writes staged bytes to a tmp file QuickLook can open. The filename's
    /// extension drives type detection, so ensure one exists (derived from
    /// the mime type when the name has none).
    private static func writeToTemp(data: Data, filename: String, mimeType: String?) throws -> URL {
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("DocumentPreviews", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)

        var safeName = filename
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "\\", with: "_")
        if safeName.isEmpty { safeName = "document" }
        if (safeName as NSString).pathExtension.isEmpty {
            safeName += Self.fallbackExtension(for: mimeType)
        }

        let url = dir.appendingPathComponent(safeName)
        try data.write(to: url, options: .atomic)
        return url
    }

    private static func fallbackExtension(for mimeType: String?) -> String {
        switch mimeType {
        case "application/pdf": return ".pdf"
        case "image/png": return ".png"
        case "image/jpeg": return ".jpg"
        case "image/heic": return ".heic"
        default: return ".dat"
        }
    }
}

// MARK: - QuickLook wrapper

/// Bare `QLPreviewController` for a single local file, embeddable inside a
/// sheet (unlike `.quickLookPreview`, which presents its own overlay).
private struct QuickLookPreview: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> QLPreviewController {
        let controller = QLPreviewController()
        controller.dataSource = context.coordinator
        return controller
    }

    func updateUIViewController(_ controller: QLPreviewController, context: Context) {
        context.coordinator.url = url
        controller.reloadData()
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(url: url)
    }

    final class Coordinator: NSObject, QLPreviewControllerDataSource {
        var url: URL

        init(url: URL) {
            self.url = url
        }

        func numberOfPreviewItems(in controller: QLPreviewController) -> Int { 1 }

        func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> QLPreviewItem {
            url as NSURL
        }
    }
}

// MARK: - Zoomable image

/// Pinch-to-zoom / drag image viewer used for the local-image fast path.
private struct ZoomableImageView: View {
    let image: UIImage

    @State private var scale: CGFloat = 1
    @State private var lastScale: CGFloat = 1
    @State private var offset: CGSize = .zero
    @State private var lastOffset: CGSize = .zero

    var body: some View {
        GeometryReader { proxy in
            Image(uiImage: image)
                .resizable()
                .scaledToFit()
                .frame(width: proxy.size.width, height: proxy.size.height)
                .scaleEffect(scale)
                .offset(offset)
                .gesture(
                    MagnificationGesture()
                        .onChanged { value in
                            scale = min(max(lastScale * value, 1), 6)
                        }
                        .onEnded { _ in
                            lastScale = scale
                            if scale <= 1.01 {
                                withAnimation(.easeOut(duration: 0.15)) {
                                    scale = 1
                                    offset = .zero
                                }
                                lastScale = 1
                                lastOffset = .zero
                            }
                        }
                )
                .simultaneousGesture(
                    DragGesture()
                        .onChanged { value in
                            guard scale > 1 else { return }
                            offset = CGSize(
                                width: lastOffset.width + value.translation.width,
                                height: lastOffset.height + value.translation.height
                            )
                        }
                        .onEnded { _ in
                            lastOffset = offset
                        }
                )
                .onTapGesture(count: 2) {
                    withAnimation(.easeInOut(duration: 0.2)) {
                        if scale > 1 {
                            scale = 1
                            offset = .zero
                        } else {
                            scale = 2.5
                        }
                    }
                    lastScale = scale
                    lastOffset = offset
                }
        }
        .background(Color(.systemBackground))
    }
}
