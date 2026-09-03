import SwiftUI

struct ChatInputBar: View {
    @Binding var text: String
    let onSend: () -> Void
    let onRequestTap: () -> Void

    // The keyboard drops on send (client fix batch, item 8) — the field
    // was permanently focused with no way to dismiss on any chat surface.
    @FocusState private var inputFocused: Bool

    var body: some View {
        VStack(spacing: 0) {
            Divider()
            HStack(spacing: 8) {
                // Request creation button
                Button(action: onRequestTap) {
                    Image(systemName: "plus.circle.fill")
                        .font(.title2)
                        .foregroundColor(.driveBaiPrimary)
                }

                // Text field
                TextField("Type a message...", text: $text, axis: .vertical)
                    .lineLimit(1...4)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(Color(.systemGray6))
                    .clipShape(RoundedRectangle(cornerRadius: 20))
                    .focused($inputFocused)

                // Send button
                Button(action: {
                    inputFocused = false
                    onSend()
                }) {
                    Image(systemName: "arrow.up.circle.fill")
                        .font(.title2)
                        .foregroundColor(text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? .gray : .driveBaiPrimary)
                }
                .disabled(text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
        }
        .background(Color(.systemBackground))
    }
}
