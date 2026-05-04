import SwiftUI

struct LeaseRequestCardView: View {
    let leaseRequest: LeaseRequest
    let currentUserId: UUID
    let onAccept: () -> Void
    let onDecline: () -> Void
    let onPay: () -> Void
    let onCancel: () -> Void
    let onAdjustPrice: () -> Void

    private var isOwner: Bool { currentUserId == leaseRequest.ownerId }
    private var isDriver: Bool { currentUserId == leaseRequest.driverId }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            // Header: car icon + title + status badge
            HStack {
                Image(systemName: "car.fill")
                    .font(.title3)
                    .foregroundColor(.driveBaiPrimary)
                    .frame(width: 32, height: 32)
                    .background(Color.driveBaiPrimary.opacity(0.12))
                    .clipShape(RoundedRectangle(cornerRadius: 8))

                VStack(alignment: .leading, spacing: 2) {
                    Text(leaseRequest.carTitle)
                        .font(.headline)
                        .lineLimit(1)

                    Text("Lease Request")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }

                Spacer()

                statusBadge
            }

            // Message
            if let message = leaseRequest.message, !message.isEmpty {
                Text(message)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .lineLimit(3)
            }

            // Price-adjusted banner (driver only, while still pending)
            if isDriver && leaseRequest.isPriceAdjusted && leaseRequest.status == .requested {
                HStack(spacing: 6) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundColor(.orange)
                    Text("Price updated by owner")
                        .font(.caption.weight(.medium))
                        .foregroundColor(.orange)
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color.orange.opacity(0.1))
                .clipShape(RoundedRectangle(cornerRadius: 8))
            }

            // Pricing details
            HStack(spacing: 16) {
                Label {
                    if leaseRequest.isPriceAdjusted, let base = leaseRequest.formattedWeeklyPrice,
                       let offered = leaseRequest.formattedEffectiveWeeklyPrice {
                        HStack(spacing: 4) {
                            Text("\(base)/wk")
                                .strikethrough()
                                .foregroundColor(.secondary)
                            Text("\(offered)/wk")
                                .font(.subheadline.weight(.semibold))
                                .foregroundColor(.primary)
                        }
                    } else if let weeklyFormatted = leaseRequest.formattedEffectiveWeeklyPrice {
                        Text("\(weeklyFormatted)/wk")
                            .font(.subheadline.weight(.semibold))
                    }
                } icon: {
                    Image(systemName: "calendar")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }

                Text("\(leaseRequest.weeks) \(leaseRequest.weeks == 1 ? "week" : "weeks")")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            }

            // Total amount
            if let totalFormatted = leaseRequest.formattedTotalAmount {
                HStack(spacing: 4) {
                    Image(systemName: "banknote")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    Text("Total: \(totalFormatted)")
                        .font(.subheadline.weight(.semibold))
                }
            }

            // Participants
            HStack {
                Text(isOwner ? "From \(leaseRequest.driverName)" : "To \(leaseRequest.ownerName)")
                    .font(.caption)
                    .foregroundColor(.secondary)

                Spacer()

                Text(leaseRequest.createdAt, style: .relative)
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            // Action buttons based on role + status
            actionButtons
        }
        .padding()
        .background(Color(.systemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .shadow(color: .black.opacity(0.06), radius: 6, x: 0, y: 2)
    }

    // MARK: - Status Badge

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
        // Show "Paid" badge if payment succeeded even if lease status hasn't caught up
        if leaseRequest.status == .paid || leaseRequest.isPaymentSucceededAwaitingWebhook {
            return ("Paid", .green)
        }
        switch leaseRequest.status {
        case .requested: return ("Requested", .blue)
        case .accepted: return ("Accepted", .green)
        case .declined: return ("Declined", .red)
        case .cancelled: return ("Cancelled", .gray)
        case .paymentPending:
            if leaseRequest.isPaymentProcessing {
                return ("Processing", .orange)
            }
            return ("Payment Pending", .orange)
        case .paid: return ("Paid", .green)
        case .expired: return ("Expired", .gray)
        }
    }

    // MARK: - Action Buttons

    @ViewBuilder
    private var actionButtons: some View {
        if isOwner && leaseRequest.status.ownerCanRespond {
            // Owner: adjust price + accept/decline
            Button(action: onAdjustPrice) {
                HStack(spacing: 6) {
                    Image(systemName: "pencil")
                    Text(leaseRequest.isPriceAdjusted ? "Adjust Price Again" : "Adjust Price")
                }
                .font(.subheadline.weight(.medium))
                .frame(maxWidth: .infinity)
                .padding(.vertical, 10)
                .background(Color(.systemGray6))
                .foregroundColor(.primary)
                .clipShape(RoundedRectangle(cornerRadius: 10))
            }

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
        } else if isDriver && (leaseRequest.status == .paid || leaseRequest.isPaymentSucceededAwaitingWebhook) {
            // Payment succeeded — show confirmation
            HStack(spacing: 6) {
                Image(systemName: "checkmark.circle.fill")
                Text("Payment Complete")
            }
            .font(.subheadline.weight(.medium))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .foregroundColor(.green)
        } else if isDriver && leaseRequest.isPaymentProcessing {
            // Payment in-flight — show processing indicator
            HStack(spacing: 6) {
                ProgressView()
                    .scaleEffect(0.8)
                Text("Processing Payment...")
            }
            .font(.subheadline.weight(.medium))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .foregroundColor(.orange)
        } else if isDriver && leaseRequest.driverCanPay {
            // Driver can pay (accepted, or payment_pending with failed/canceled payment)
            let isRetry = leaseRequest.status == .paymentPending
            Button(action: onPay) {
                HStack(spacing: 6) {
                    Image(systemName: "creditcard.fill")
                    Text(isRetry ? "Retry Payment" : "Pay Now")
                }
                .font(.subheadline.weight(.medium))
                .frame(maxWidth: .infinity)
                .padding(.vertical, 10)
                .background(Color.driveBaiPrimary)
                .foregroundColor(.white)
                .clipShape(RoundedRectangle(cornerRadius: 10))
            }
        } else if isDriver && leaseRequest.status.driverCanCancel {
            // Driver can cancel
            Button(action: onCancel) {
                Text("Cancel Request")
                    .font(.subheadline.weight(.medium))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 10)
                    .background(Color(.systemGray5))
                    .foregroundColor(.red)
                    .clipShape(RoundedRectangle(cornerRadius: 10))
            }
        }
    }
}
