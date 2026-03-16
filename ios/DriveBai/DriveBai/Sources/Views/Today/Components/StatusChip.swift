import SwiftUI

/// A small status chip/badge for listing cards
struct StatusChip: View {
    let status: ListingStatus

    private var backgroundColor: Color {
        switch status {
        case .active:
            return Color.green.opacity(0.15)
        case .rented:
            return Color.blue.opacity(0.15)
        case .pending:
            return Color.orange.opacity(0.15)
        case .paused:
            return Color.gray.opacity(0.15)
        }
    }

    private var textColor: Color {
        switch status {
        case .active:
            return .green
        case .rented:
            return .blue
        case .pending:
            return .orange
        case .paused:
            return .gray
        }
    }

    var body: some View {
        HStack(spacing: 4) {
            Image(systemName: "checkmark")
                .font(.caption2)
            Text("Car status")
                .font(.caption2)
                .fontWeight(.semibold)
        }
        .foregroundColor(.primary)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(Color(.systemBackground))
        .cornerRadius(4)
    }
}

#Preview {
    HStack(spacing: 12) {
        StatusChip(status: .active)
        StatusChip(status: .rented)
        StatusChip(status: .pending)
        StatusChip(status: .paused)
    }
    .padding()
}
