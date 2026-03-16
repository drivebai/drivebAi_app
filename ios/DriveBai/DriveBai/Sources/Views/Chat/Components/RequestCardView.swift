import SwiftUI

struct RequestCardView: View {
    let request: ChatRequest
    let currentUserId: UUID
    let onAccept: () -> Void
    let onDecline: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            // Header: type icon + title + status badge
            HStack {
                Image(systemName: request.type.iconName)
                    .font(.title3)
                    .foregroundColor(.driveBaiPrimary)
                    .frame(width: 32, height: 32)
                    .background(Color.driveBaiPrimary.opacity(0.12))
                    .clipShape(RoundedRectangle(cornerRadius: 8))

                VStack(alignment: .leading, spacing: 2) {
                    Text(request.title)
                        .font(.headline)
                        .lineLimit(1)

                    Text(request.type.displayTitle)
                        .font(.caption)
                        .foregroundColor(.secondary)
                }

                Spacer()

                statusBadge
            }

            // Description
            if !request.description.isEmpty {
                Text(request.description)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .lineLimit(3)
            }

            // Amount
            if let formatted = request.formattedAmount {
                HStack(spacing: 4) {
                    Image(systemName: "banknote")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    Text(formatted)
                        .font(.subheadline.weight(.semibold))
                }
            }

            // Countdown + created by
            HStack {
                if request.status == .pending && !request.isExpired {
                    let remaining = request.remainingTime
                    Label {
                        if remaining.days > 0 {
                            Text("\(remaining.days)d \(remaining.hours)h left")
                        } else if remaining.hours > 0 {
                            Text("\(remaining.hours)h \(remaining.minutes)m left")
                        } else {
                            Text("\(remaining.minutes)m left")
                        }
                    } icon: {
                        Image(systemName: "clock")
                    }
                    .font(.caption)
                    .foregroundColor(remaining.hours < 2 && remaining.days == 0 ? .orange : .secondary)
                }

                Spacer()

                Text("by \(request.createdByName)")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            // Action buttons (only for target user on pending requests)
            if request.isActionable && request.targetUserId == currentUserId {
                HStack(spacing: 12) {
                    Button(action: onDecline) {
                        Text("Decline")
                            .font(.subheadline.weight(.medium))
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 10)
                            .background(Color(.systemGray5))
                            .foregroundColor(.primary)
                            .clipShape(RoundedRectangle(cornerRadius: 10))
                    }

                    Button(action: onAccept) {
                        Text("Accept")
                            .font(.subheadline.weight(.medium))
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 10)
                            .background(Color.driveBaiPrimary)
                            .foregroundColor(.white)
                            .clipShape(RoundedRectangle(cornerRadius: 10))
                    }
                }
            }

            // Cancel button (only for creator on pending requests)
            if request.isActionable && request.createdById == currentUserId {
                Text("You sent this request")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            // Response note
            if let note = request.responseNote, !note.isEmpty {
                HStack(spacing: 6) {
                    Image(systemName: "text.bubble")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    Text(note)
                        .font(.caption)
                        .foregroundColor(.secondary)
                        .italic()
                }
            }
        }
        .padding()
        .background(Color(.systemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .shadow(color: .black.opacity(0.06), radius: 6, x: 0, y: 2)
    }

    @ViewBuilder
    private var statusBadge: some View {
        let (text, color) = statusInfo
        Text(text)
            .font(.caption.weight(.medium))
            .foregroundColor(color)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(color.opacity(0.12))
            .clipShape(Capsule())
    }

    private var statusInfo: (String, Color) {
        if request.isExpired && request.status == .pending {
            return ("Expired", .orange)
        }
        switch request.status {
        case .pending: return ("Pending", .blue)
        case .accepted: return ("Accepted", .green)
        case .declined: return ("Declined", .red)
        case .expired: return ("Expired", .orange)
        case .cancelled: return ("Cancelled", .gray)
        }
    }
}
