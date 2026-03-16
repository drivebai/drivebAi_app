import SwiftUI

struct ChatBubbleView: View {
    let message: ChatMessage

    var body: some View {
        if message.isSystem {
            systemMessageView
        } else {
            messageBubble
        }
    }

    private var systemMessageView: some View {
        HStack {
            Spacer()
            Text(message.body)
                .font(.caption)
                .foregroundColor(.secondary)
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
                .background(Color(.systemGray6))
                .clipShape(Capsule())
            Spacer()
        }
        .padding(.vertical, 4)
    }

    private var messageBubble: some View {
        HStack(alignment: .bottom, spacing: 8) {
            if message.direction == .sent { Spacer(minLength: 60) }

            VStack(alignment: message.direction == .sent ? .trailing : .leading, spacing: 2) {
                Text(message.body)
                    .font(.body)
                    .foregroundColor(message.direction == .sent ? .white : .primary)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 10)
                    .background(
                        message.direction == .sent
                            ? Color.driveBaiPrimary
                            : Color(.systemGray5)
                    )
                    .clipShape(RoundedRectangle(cornerRadius: 18))

                HStack(spacing: 4) {
                    Text(formatTime(message.createdAt))
                        .font(.caption2)
                        .foregroundColor(.secondary)

                    if message.direction == .sent {
                        statusIcon
                    }
                }
            }

            if message.direction == .received { Spacer(minLength: 60) }
        }
        .padding(.vertical, 1)
    }

    @ViewBuilder
    private var statusIcon: some View {
        switch message.status {
        case .sending:
            Image(systemName: "clock")
                .font(.caption2)
                .foregroundColor(.secondary)
        case .sent:
            Image(systemName: "checkmark")
                .font(.caption2)
                .foregroundColor(.secondary)
        case .failed:
            Image(systemName: "exclamationmark.circle")
                .font(.caption2)
                .foregroundColor(.red)
        }
    }

    private func formatTime(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "h:mm a"
        return formatter.string(from: date)
    }
}
