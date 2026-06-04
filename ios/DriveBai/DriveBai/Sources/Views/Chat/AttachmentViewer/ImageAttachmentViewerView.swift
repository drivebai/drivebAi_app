import SwiftUI
import os

/// Full-screen image attachment viewer presented when the user taps an image
/// bubble in chat. Downloads the file to local caches via
/// `AttachmentDownloadService` (which adds Bearer auth if available), decodes
/// it on a background queue, and offers Share + Save to Photos actions.
struct ImageAttachmentViewerView: View {
    let attachment: ChatAttachment

    @Environment(\.dismiss) private var dismiss

    @State private var localURL: URL?
    @State private var displayImage: UIImage?
    @State private var error: String?
    @State private var savingToPhotos = false
    @State private var saveResult: SaveResult?
    @State private var scale: CGFloat = 1.0
    @State private var lastScale: CGFloat = 1.0
    @State private var offset: CGSize = .zero
    @State private var lastOffset: CGSize = .zero

    private static let logger = Logger(subsystem: "com.drivebai", category: "ImageViewer")

    enum SaveResult: Identifiable {
        case success
        case failure(String)
        var id: String {
            switch self {
            case .success: return "ok"
            case .failure(let m): return "err:" + m
            }
        }
    }

    var body: some View {
        ZStack(alignment: .topTrailing) {
            Color.black.ignoresSafeArea()

            content
                .ignoresSafeArea()

            toolbar
        }
        .task { await fetchIfNeeded() }
        .alert(item: $saveResult) { result in
            switch result {
            case .success:
                return Alert(title: Text("Saved to Photos"), dismissButton: .default(Text("OK")))
            case .failure(let message):
                return Alert(title: Text("Couldn't save"), message: Text(message),
                             dismissButton: .default(Text("OK")))
            }
        }
    }

    // MARK: Content

    @ViewBuilder
    private var content: some View {
        if let image = displayImage {
            // Why the explicit max-frame: the parent ZStack uses
            // `alignment: .topTrailing` so the toolbar can hug the top-right.
            // Without filling the available space, a `scaledToFit` image
            // shrinks to its natural fit size and inherits that .topTrailing
            // alignment from the ZStack — pinning portrait photos to the top
            // of the screen. Filling the frame and letting `scaledToFit`
            // center the bitmap inside it restores true vertical centering.
            Image(uiImage: image)
                .resizable()
                .scaledToFit()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .scaleEffect(scale)
                .offset(offset)
                .gesture(zoomGesture)
                .simultaneousGesture(panGesture)
                .onTapGesture(count: 2) {
                    withAnimation(.easeInOut(duration: 0.2)) {
                        if scale > 1.0 {
                            scale = 1.0
                            offset = .zero
                            lastOffset = .zero
                        } else {
                            scale = 2.0
                        }
                        lastScale = scale
                    }
                }
        } else if let error {
            VStack(spacing: 14) {
                Image(systemName: "exclamationmark.triangle")
                    .font(.largeTitle)
                    .foregroundColor(.white.opacity(0.7))
                Text(error)
                    .font(.subheadline)
                    .foregroundColor(.white.opacity(0.8))
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 24)
                Button("Retry") {
                    Task { await fetchIfNeeded(force: true) }
                }
                .buttonStyle(.borderedProminent)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            ProgressView()
                .tint(.white)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    // MARK: Toolbar

    private var toolbar: some View {
        HStack(spacing: 10) {
            Spacer()

            if let localURL {
                ShareLink(item: localURL) {
                    iconButton(systemName: "square.and.arrow.up")
                }
                .accessibilityLabel("Share")

                Button {
                    Task { await saveToPhotos(from: localURL) }
                } label: {
                    if savingToPhotos {
                        ProgressView().tint(.white)
                            .frame(width: 32, height: 32)
                            .background(Color.black.opacity(0.45))
                            .clipShape(Circle())
                    } else {
                        iconButton(systemName: "square.and.arrow.down")
                    }
                }
                .accessibilityLabel("Save to Photos")
                .disabled(savingToPhotos)
            }

            Button(action: { dismiss() }) {
                iconButton(systemName: "xmark")
            }
            .accessibilityLabel("Close")
        }
        .padding(.horizontal, 12)
        .padding(.top, 8)
    }

    private func iconButton(systemName: String) -> some View {
        Image(systemName: systemName)
            .font(.system(size: 16, weight: .semibold))
            .foregroundColor(.white)
            .frame(width: 32, height: 32)
            .background(Color.black.opacity(0.45))
            .clipShape(Circle())
    }

    // MARK: Gestures

    private var zoomGesture: some Gesture {
        MagnificationGesture()
            .onChanged { value in
                scale = max(1.0, min(lastScale * value, 5.0))
            }
            .onEnded { _ in
                lastScale = scale
                if scale < 1.05 {
                    withAnimation(.easeOut(duration: 0.15)) {
                        scale = 1.0
                        lastScale = 1.0
                        // When the zoom snaps back to 1× the pan offset is no
                        // longer meaningful — re-center the image.
                        offset = .zero
                        lastOffset = .zero
                    }
                }
            }
    }

    private var panGesture: some Gesture {
        DragGesture()
            .onChanged { value in
                guard scale > 1.0 else { return }
                offset = CGSize(
                    width: lastOffset.width + value.translation.width,
                    height: lastOffset.height + value.translation.height
                )
            }
            .onEnded { _ in lastOffset = offset }
    }

    // MARK: Download + Save

    private func fetchIfNeeded(force: Bool = false) async {
        if displayImage != nil && !force { return }
        guard let remote = attachment.fullFileURL else {
            self.error = "This attachment has no remote URL."
            return
        }
        error = nil
        displayImage = nil
        do {
            let local = try await AttachmentDownloadService.shared.download(
                url: remote,
                suggestedFilename: attachment.filename
            )
            localURL = local

            let decoded = await Task.detached(priority: .userInitiated) {
                UIImage(contentsOfFile: local.path)
            }.value

            if let decoded {
                displayImage = decoded
            } else {
                error = "Couldn't decode the image."
            }
        } catch {
            Self.logger.error("Image download failed: \(error.localizedDescription)")
            self.error = error.localizedDescription
        }
    }

    private func saveToPhotos(from local: URL) async {
        savingToPhotos = true
        defer { savingToPhotos = false }
        do {
            let data = try Data(contentsOf: local)
            try await PhotoLibrarySaver.save(data: data)
            saveResult = .success
        } catch let saveErr as PhotoLibrarySaver.SaveError {
            saveResult = .failure(saveErr.errorDescription ?? "Unknown error.")
        } catch {
            saveResult = .failure(error.localizedDescription)
        }
    }
}
