import SwiftUI

// MARK: - Ticket wizard (category → describe → attach → review)
//
// A lighter sibling of the accident report: a server-side draft born on open,
// evidence uploaded against it, autosaved per step (so killing the app and
// reopening resumes exactly where you were), then a validated submit.
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
                    VStack(spacing: 0) {
                        progressHeader
                        ScrollView {
                            stepContent
                                .padding(16)
                                .padding(.bottom, 90)
                        }
                    }
                    .safeAreaInset(edge: .bottom) { ctaBar }
                }
            }
            .navigationTitle("New request")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if !vm.isSubmitted {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") { dismiss() }
                    }
                }
            }
            .task { await vm.loadOrCreate() }
            .alert("Something went wrong", isPresented: Binding(
                get: { vm.error != nil }, set: { if !$0 { vm.error = nil } }
            )) { Button("OK", role: .cancel) { vm.error = nil } } message: { Text(vm.error ?? "") }
        }
    }

    // MARK: Chrome

    private var progressHeader: some View {
        VStack(spacing: 8) {
            HStack {
                Text("Step \(vm.currentStep.rawValue + 1) of \(TicketStep.allCases.count)")
                    .font(.caption).foregroundColor(.secondary)
                Spacer()
                if vm.isSaving {
                    HStack(spacing: 4) {
                        ProgressView().scaleEffect(0.7)
                        Text("Saving…").font(.caption2).foregroundColor(.secondary)
                    }
                }
            }
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color(.systemGray5)).frame(height: 4)
                    Capsule().fill(Color.driveBaiPrimary)
                        .frame(width: geo.size.width * progressFraction, height: 4)
                }
            }
            .frame(height: 4)
        }
        .padding(.horizontal, 16)
        .padding(.top, 8)
    }

    private var progressFraction: CGFloat {
        CGFloat(vm.currentStep.rawValue + 1) / CGFloat(TicketStep.allCases.count)
    }

    private var ctaBar: some View {
        VStack(spacing: 0) {
            Divider()
            HStack(spacing: 12) {
                if vm.canGoBack {
                    Button("Back") { vm.goBack() }
                        .buttonStyle(.bordered)
                        .tint(.secondary)
                }
                Button(action: { Task { vm.isLastStep ? await vm.submit() : await vm.goForward() } }) {
                    HStack {
                        if vm.isSaving || vm.isSubmitting { ProgressView().tint(.white) }
                        Text(vm.isLastStep ? "Submit request" : "Next").fontWeight(.semibold)
                    }
                    .frame(maxWidth: .infinity)
                }
                .buttonStyle(DriveBaiButtonStyle())
                .disabled(!vm.canAdvance || vm.isSaving || vm.isSubmitting)
            }
            .padding(16)
            .background(Color(.systemBackground))
        }
    }

    // MARK: Steps

    @ViewBuilder
    private var stepContent: some View {
        switch vm.currentStep {
        case .category: categoryStep
        case .describe: describeStep
        case .attach:   attachStep
        case .review:   reviewStep
        }
    }

    private var categoryStep: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(vm.currentStep.title).font(.title3).fontWeight(.bold)
            Text("Pick the closest match so we can route your request.")
                .font(.subheadline).foregroundColor(.secondary)
            LazyVGrid(columns: [GridItem(.flexible(), spacing: 12), GridItem(.flexible(), spacing: 12)], spacing: 12) {
                ForEach(TicketCategory.allCases) { cat in
                    CategoryCard(category: cat, isSelected: vm.category == cat) {
                        vm.category = cat
                    }
                }
            }
        }
    }

    private var describeStep: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(vm.currentStep.title).font(.title3).fontWeight(.bold)
            VStack(alignment: .leading, spacing: 6) {
                Text("Subject").font(.caption.weight(.medium)).foregroundColor(.secondary)
                TextField("Short summary (optional)", text: $vm.subject)
                    .textFieldStyle(.roundedBorder)
            }
            VStack(alignment: .leading, spacing: 6) {
                Text("What's happening?").font(.caption.weight(.medium)).foregroundColor(.secondary)
                TextEditor(text: $vm.descriptionText)
                    .frame(minHeight: 160)
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
        }
    }

    private var attachStep: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(vm.currentStep.title).font(.title3).fontWeight(.bold)
            Text("Screenshots or documents help us understand faster. Optional.")
                .font(.subheadline).foregroundColor(.secondary)

            LazyVGrid(columns: [GridItem(.adaptive(minimum: 90), spacing: 10)], spacing: 10) {
                ForEach(vm.attachments) { att in
                    AttachmentThumb(attachment: att) { Task { await vm.deleteAttachment(id: att.id) } }
                }
                AddEvidenceButton(isUploading: vm.isUploadingAttachment) { picked in
                    Task { await vm.uploadAttachment(data: picked.data, filename: picked.filename, mimeType: picked.mimeType) }
                }
            }
        }
    }

    private var reviewStep: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(vm.currentStep.title).font(.title3).fontWeight(.bold)
            reviewRow("Category", vm.category?.label ?? "—")
            if !vm.subject.isEmpty { reviewRow("Subject", vm.subject) }
            reviewRow("Details", vm.descriptionText.isEmpty ? "—" : vm.descriptionText)
            reviewRow("Evidence", vm.attachments.isEmpty ? "None" : "\(vm.attachments.count) attached")
            Text("Support is a small team — we read every request and reply as soon as we can.")
                .font(.footnote).foregroundColor(.secondary)
                .padding(.top, 4)
        }
    }

    private func reviewRow(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(label).font(.caption.weight(.medium)).foregroundColor(.secondary)
            Text(value).font(.body).foregroundColor(.primary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
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

private struct CategoryCard: View {
    let category: TicketCategory
    let isSelected: Bool
    let onTap: () -> Void

    var body: some View {
        Button(action: onTap) {
            VStack(alignment: .leading, spacing: 8) {
                Image(systemName: category.icon)
                    .font(.system(size: 22))
                    .foregroundColor(.driveBaiPrimary)
                Text(category.label).font(.subheadline.weight(.semibold))
                    .foregroundColor(.primary).multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
                Text(category.blurb).font(.caption2).foregroundColor(.secondary)
                    .multilineTextAlignment(.leading).fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity, minHeight: 120, alignment: .topLeading)
            .padding(12)
            .background(RoundedRectangle(cornerRadius: 14).fill(Color(.systemGray6)))
            .overlay(
                RoundedRectangle(cornerRadius: 14)
                    .stroke(isSelected ? Color.driveBaiPrimary : Color.clear, lineWidth: 2.5)
            )
        }
        .buttonStyle(.plain)
        .accessibilityLabel(category.label)
        .accessibilityAddTraits(isSelected ? [.isSelected] : [])
    }
}

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
