import SwiftUI

// MARK: - Report a Problem (one screen)
//
// A lighter sibling of the accident report, collapsed from a 4-step wizard to
// a single screen: category dropdown, description, evidence, Submit. The
// server-side draft is still born on open — evidence can only upload against
// an existing ticket id — and the form persists into it on close, so an
// abandoned report comes back filled.
struct TicketFlowView: View {
    @StateObject private var vm = TicketFlowViewModel()
    @Environment(\.dismiss) private var dismiss
    /// Called after a successful submit so the caller (the hub) can refresh
    /// "My requests".
    var onSubmitted: () -> Void = {}

    var body: some View {
        NavigationStack {
            Group {
                if vm.isSubmitted {
                    submittedScreen
                } else if vm.isLoading && vm.ticket == nil {
                    ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    ScrollView {
                        formContent
                            .padding(16)
                            .padding(.bottom, 90)
                    }
                    .safeAreaInset(edge: .bottom) { ctaBar }
                }
            }
            .navigationTitle("Report a Problem")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if !vm.isSubmitted {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") { dismiss() }
                    }
                }
            }
            .task { await vm.loadOrCreate() }
            // Persist whatever was typed when the sheet goes away (Cancel or
            // swipe-down) so the draft resumes filled next time.
            .onDisappear {
                if !vm.isSubmitted {
                    Task { await vm.saveDraft() }
                }
            }
            .alert("Something went wrong", isPresented: Binding(
                get: { vm.error != nil }, set: { if !$0 { vm.error = nil } }
            )) { Button("OK", role: .cancel) { vm.error = nil } } message: { Text(vm.error ?? "") }
        }
    }

    // MARK: Form

    private var formContent: some View {
        VStack(alignment: .leading, spacing: 20) {
            // Category dropdown. "Other" reveals the free-text topic line.
            VStack(alignment: .leading, spacing: 6) {
                Text("Category").font(.caption.weight(.medium)).foregroundColor(.secondary)
                Menu {
                    ForEach(TicketCategory.allCases) { cat in
                        Button {
                            vm.category = cat
                        } label: {
                            Label(cat.label, systemImage: cat.icon)
                        }
                    }
                } label: {
                    HStack(spacing: 8) {
                        if let cat = vm.category {
                            Image(systemName: cat.icon)
                                .font(.system(size: 16))
                                .foregroundColor(.driveBaiPrimary)
                            Text(cat.label).foregroundColor(.primary)
                        } else {
                            Text("Choose a category")
                                .foregroundColor(Color(.placeholderText))
                        }
                        Spacer()
                        Image(systemName: "chevron.up.chevron.down")
                            .font(.caption.weight(.semibold))
                            .foregroundColor(.secondary)
                    }
                    .padding(12)
                    .background(RoundedRectangle(cornerRadius: 10).fill(Color(.systemGray6)))
                }
                if let cat = vm.category {
                    Text(cat.blurb).font(.caption2).foregroundColor(.secondary)
                }
            }

            if vm.category == .other {
                VStack(alignment: .leading, spacing: 6) {
                    Text("What's it about?").font(.caption.weight(.medium)).foregroundColor(.secondary)
                    TextField("A few words (optional)", text: $vm.subject)
                        .textFieldStyle(.roundedBorder)
                }
            }

            VStack(alignment: .leading, spacing: 6) {
                Text("What's happening?").font(.caption.weight(.medium)).foregroundColor(.secondary)
                TextEditor(text: $vm.descriptionText)
                    .frame(minHeight: 140)
                    .padding(8)
                    .overlay(RoundedRectangle(cornerRadius: 10).stroke(Color(.systemGray4)))
                    .overlay(alignment: .topLeading) {
                        if vm.descriptionText.isEmpty {
                            Text("Describe the problem in as much detail as you can.")
                                .foregroundColor(Color(.placeholderText))
                                .padding(.horizontal, 13).padding(.vertical, 16)
                                .allowsHitTesting(false)
                        }
                    }
            }

            VStack(alignment: .leading, spacing: 8) {
                Text("Evidence").font(.caption.weight(.medium)).foregroundColor(.secondary)
                Text("Screenshots or documents help us understand faster. Optional.")
                    .font(.caption2).foregroundColor(.secondary)
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 90), spacing: 10)], spacing: 10) {
                    ForEach(vm.attachments) { att in
                        AttachmentThumb(attachment: att) { Task { await vm.deleteAttachment(id: att.id) } }
                    }
                    AddEvidenceButton(isUploading: vm.isUploadingAttachment) { picked in
                        Task { await vm.uploadAttachment(data: picked.data, filename: picked.filename, mimeType: picked.mimeType) }
                    }
                }
            }

            Text("Support is a small team — we read every request and reply as soon as we can.")
                .font(.footnote).foregroundColor(.secondary)
                .padding(.top, 4)
        }
    }

    private var ctaBar: some View {
        VStack(spacing: 0) {
            Divider()
            Button(action: { Task { await vm.submit() } }) {
                HStack {
                    if vm.isSaving || vm.isSubmitting { ProgressView().tint(.white) }
                    Text("Submit").fontWeight(.semibold)
                }
                .frame(maxWidth: .infinity)
            }
            .buttonStyle(DriveBaiButtonStyle())
            .disabled(!vm.canSubmit || vm.isSaving || vm.isSubmitting)
            .padding(16)
            .background(Color(.systemBackground))
        }
    }

    // MARK: Submitted

    private var submittedScreen: some View {
        VStack(spacing: 18) {
            Spacer()
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 64)).foregroundColor(.driveBaiPrimary)
            Text("Request sent").font(.title2).fontWeight(.bold)
            Text("We've got your request and will reply in your support chat. You can track it in My requests.")
                .font(.subheadline).foregroundColor(.secondary)
                .multilineTextAlignment(.center).padding(.horizontal, 32)
            Spacer()
            Button("Done") { onSubmitted(); dismiss() }
                .buttonStyle(DriveBaiButtonStyle())
                .padding(.horizontal, 24).padding(.bottom, 24)
        }
    }
}

// MARK: - Subviews

private struct AttachmentThumb: View {
    let attachment: TicketAttachmentAPI
    let onDelete: () -> Void

    private var isImage: Bool { attachment.mimeType.hasPrefix("image/") }
    private var url: URL? { URL(string: AppConfig.serverBaseURL.absoluteString + attachment.fileUrl) }

    var body: some View {
        ZStack(alignment: .topTrailing) {
            Group {
                if isImage, let url {
                    RemoteImage(url: url, contentMode: .fill, maxPixelSize: 300)
                } else {
                    VStack(spacing: 4) {
                        Image(systemName: "doc.fill").font(.system(size: 22)).foregroundColor(.driveBaiPrimary)
                        Text("PDF").font(.caption2).foregroundColor(.secondary)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(Color(.systemGray6))
                }
            }
            .frame(width: 90, height: 90)
            .clipShape(RoundedRectangle(cornerRadius: 10))

            Button(action: onDelete) {
                Image(systemName: "xmark.circle.fill")
                    .font(.system(size: 18))
                    .foregroundStyle(.white, Color.black.opacity(0.5))
            }
            .padding(3)
        }
    }
}

private struct AddEvidenceButton: View {
    let isUploading: Bool
    let onPicked: (PickedDocument) -> Void
    @State private var showPicker = false

    var body: some View {
        Button(action: { showPicker = true }) {
            VStack(spacing: 6) {
                if isUploading {
                    ProgressView()
                } else {
                    Image(systemName: "plus").font(.system(size: 22, weight: .semibold))
                    Text("Add").font(.caption2)
                }
            }
            .foregroundColor(.driveBaiPrimary)
            .frame(width: 90, height: 90)
            .background(RoundedRectangle(cornerRadius: 10).fill(Color.driveBaiPrimary.opacity(0.08)))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(Color.driveBaiPrimary.opacity(0.3), style: StrokeStyle(lineWidth: 1.5, dash: [4])))
        }
        .buttonStyle(.plain)
        .disabled(isUploading)
        // camera + library + files, with HEIC→JPEG transcode handled inside.
        .documentSourcePicker(isPresented: $showPicker, filenameBase: "evidence", onPicked: onPicked)
    }
}
