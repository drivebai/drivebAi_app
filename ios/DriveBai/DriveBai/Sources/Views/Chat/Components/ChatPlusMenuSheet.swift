import SwiftUI

/// Actions the chat "+" button can launch.
enum ChatPlusAction {
    case createRequest
    case reportAccident
    case attachPhoto
    case attachFile
}

/// Bottom-sheet menu shown when the user taps "+" in the chat input bar.
/// The sheet only collects the user's choice and dismisses; the parent triggers
/// the actual flow in `onDismiss` to avoid the SwiftUI double-sheet race.
struct ChatPlusMenuSheet: View {
    @Environment(\.dismiss) private var dismiss
    let onChoose: (ChatPlusAction) -> Void

    var body: some View {
        VStack(spacing: 0) {
            grabber

            Text("Actions")
                .font(.title3.weight(.semibold))
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 20)
                .padding(.top, 8)
                .padding(.bottom, 12)

            VStack(spacing: 0) {
                row(icon: "doc.text",
                    iconColor: .driveBaiPrimary,
                    title: "Create Request",
                    subtitle: "Send a structured request to the other party") {
                    select(.createRequest)
                }
                divider
                row(icon: "exclamationmark.triangle.fill",
                    iconColor: .red,
                    title: "Report an Accident",
                    subtitle: "Open the accident report wizard") {
                    select(.reportAccident)
                }
                divider
                row(icon: "photo.on.rectangle",
                    iconColor: .driveBaiPrimary,
                    title: "Attach Photo",
                    subtitle: "Pick from your library") {
                    select(.attachPhoto)
                }
                divider
                row(icon: "paperclip",
                    iconColor: .driveBaiPrimary,
                    title: "Attach File",
                    subtitle: "Choose a PDF or document") {
                    select(.attachFile)
                }
            }
            .background(Color(.systemBackground))
            .cornerRadius(14)
            .overlay(
                RoundedRectangle(cornerRadius: 14)
                    .stroke(Color(.systemGray5), lineWidth: 1)
            )
            .padding(.horizontal, 16)
            .padding(.bottom, 24)

            Spacer(minLength: 0)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.systemGroupedBackground))
        .presentationDetents([.medium])
        .presentationDragIndicator(.hidden)
    }

    private var grabber: some View {
        Capsule()
            .fill(Color(.systemGray4))
            .frame(width: 36, height: 5)
            .padding(.top, 8)
    }

    private var divider: some View {
        Divider().padding(.leading, 60)
    }

    private func row(icon: String, iconColor: Color, title: String, subtitle: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 12) {
                ZStack {
                    RoundedRectangle(cornerRadius: 8)
                        .fill(iconColor.opacity(0.12))
                        .frame(width: 36, height: 36)
                    Image(systemName: icon)
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundColor(iconColor)
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.body.weight(.semibold))
                        .foregroundColor(.primary)
                    Text(subtitle)
                        .font(.caption)
                        .foregroundColor(.secondary)
                        .lineLimit(2)
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.bold))
                    .foregroundColor(Color(.systemGray3))
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    private func select(_ action: ChatPlusAction) {
        onChoose(action)
        dismiss()
    }
}
