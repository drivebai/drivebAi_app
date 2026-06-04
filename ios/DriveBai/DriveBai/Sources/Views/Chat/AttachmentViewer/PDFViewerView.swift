import SwiftUI

/// In-app PDF viewer presented as a sheet from chat attachment taps.
/// Downloads the PDF (with Bearer auth if present) into the caches directory
/// so it renders reliably even if the underlying network drops mid-view, and
/// so the user can "Save to Files" via the share sheet.
struct PDFViewerView: View {
    let attachment: ChatAttachment

    @Environment(\.dismiss) private var dismiss

    @State private var localURL: URL?
    @State private var isLoading = true
    @State private var error: String?

    var body: some View {
        NavigationStack {
            content
                .navigationTitle(attachment.filename)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Close") { dismiss() }
                    }
                    ToolbarItem(placement: .primaryAction) {
                        if let localURL {
                            ShareLink(item: localURL) {
                                Image(systemName: "square.and.arrow.up")
                            }
                            .accessibilityLabel("Share or save to Files")
                        }
                    }
                }
        }
        .task { await fetchIfNeeded() }
    }

    // MARK: Content

    @ViewBuilder
    private var content: some View {
        if let localURL {
            PDFKitRepresentedView(url: localURL)
                .ignoresSafeArea(edges: .bottom)
        } else if isLoading {
            VStack(spacing: 14) {
                ProgressView()
                Text("Downloading…")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let error {
            VStack(spacing: 14) {
                Image(systemName: "exclamationmark.triangle")
                    .font(.largeTitle)
                    .foregroundColor(.secondary)
                Text(error)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 24)
                Button("Retry") {
                    Task { await fetchIfNeeded(force: true) }
                }
                .buttonStyle(.borderedProminent)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    // MARK: Download

    private func fetchIfNeeded(force: Bool = false) async {
        if localURL != nil && !force { return }
        guard let remote = attachment.fullFileURL else {
            self.error = "This attachment has no remote URL."
            isLoading = false
            return
        }
        isLoading = true
        error = nil
        do {
            let local = try await AttachmentDownloadService.shared.download(
                url: remote,
                suggestedFilename: attachment.filename
            )
            self.localURL = local
        } catch {
            self.error = error.localizedDescription
        }
        isLoading = false
    }
}

// MARK: - Test plan (manual)
//
// 1. Sender uploads a PDF via Chat "+" → "Attach File".
// 2. Tap the PDF bubble on the sender's chat.
//    Expect: an in-app sheet titled with the filename, a Close button on the
//    left and a Share icon on the right. The PDF renders inside the sheet.
// 3. Tap Share → "Save to Files" → choose a folder. Open the Files app and
//    confirm the PDF is there and opens.
// 4. On the receiver's device, refresh / reopen the chat, tap the PDF.
//    Same in-app sheet appears with the PDF rendered.
// 5. Disable the network mid-tap. Expect the error UI with a Retry button.
