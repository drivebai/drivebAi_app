import SwiftUI

// MARK: - Car Status Pill
//
// THE one status chip for cars (QA pt 3). Renders a `CarBusinessState` in
// either of two styles:
//   - `.compact` — small caption chip for list rows (owner My Cars cards).
//   - `.hero`    — larger badge overlaid on the detail-view photo header.
//
// Layout contract (the truncation fix): the pill itself NEVER truncates.
// Its Text carries `.lineLimit(1)` + `.fixedSize(horizontal:)` +
// `.layoutPriority(1)`, so in a constrained row it keeps its intrinsic
// width and the *neighboring* metadata text is what yields/truncates.
// Callers must therefore give sibling text `.lineLimit(1)` with tail
// truncation rather than constraining the pill.

struct CarStatusPill: View {
    enum Style {
        case compact
        case hero
    }

    let state: CarBusinessState
    var style: Style = .compact

    var body: some View {
        HStack(spacing: 4) {
            Image(systemName: iconName)
                .font(.system(size: style == .compact ? 8 : 11, weight: .semibold))
            Text(text)
                .font(style == .compact ? .caption2 : .caption)
                .fontWeight(style == .compact ? .medium : .semibold)
                .lineLimit(1)
                .fixedSize(horizontal: true, vertical: false)
        }
        .layoutPriority(1)
        .foregroundColor(foregroundColor)
        .padding(.horizontal, style == .compact ? 6 : 12)
        .padding(.vertical, style == .compact ? 3 : 6)
        .background(backgroundColor)
        .cornerRadius(style == .compact ? 4 : 12)
    }

    // MARK: - Copy

    private var text: String {
        switch state {
        case .rented(let rental):
            if let rental, !firstName(of: rental.driverName).isEmpty {
                return "Rented · \(rental.weeks)w · \(firstName(of: rental.driverName))"
            }
            if let rental {
                return "Rented · \(rental.weeks)w"
            }
            return "Currently rented"
        case .available:
            return style == .hero ? "Available now!" : "Available"
        case .awaitingApproval:
            return "Awaiting approval"
        case .pendingReview:
            return "Pending approval"
        case .paused:
            return "Listing paused"
        case .sold:
            return "Sold"
        }
    }

    private func firstName(of full: String) -> String {
        full.split(separator: " ", maxSplits: 1).first.map(String.init) ?? full
    }

    // MARK: - Colors

    private var tint: Color {
        switch state {
        case .available: return .green
        case .rented: return .orange
        case .awaitingApproval: return .orange
        case .pendingReview: return .orange
        case .paused: return .gray
        case .sold: return .blue
        }
    }

    private var iconName: String {
        switch state {
        case .available: return "checkmark.circle.fill"
        case .rented: return "key.fill"
        case .awaitingApproval: return "clock.fill"
        case .pendingReview: return "clock.fill"
        case .paused: return "pause.circle.fill"
        case .sold: return "checkmark.seal.fill"
        }
    }

    private var foregroundColor: Color {
        style == .compact ? tint : .white
    }

    private var backgroundColor: Color {
        style == .compact ? tint.opacity(0.12) : tint.opacity(0.85)
    }
}

// MARK: - Previews

#Preview("Compact") {
    VStack(alignment: .leading, spacing: 8) {
        CarStatusPill(state: .available, style: .compact)
        CarStatusPill(
            state: .rented(ActiveRentalSummary(
                leaseRequestId: UUID(),
                driverId: UUID(),
                driverName: "Jonathan Applebaum",
                weeks: 4,
                weeklyPriceCents: 35000,
                pickupConfirmedAt: Date(),
                plannedEndAt: Date().addingTimeInterval(86400 * 21),
                currentEarnedCents: 70000
            )),
            style: .compact
        )
        CarStatusPill(state: .rented(nil), style: .compact)
        CarStatusPill(state: .awaitingApproval, style: .compact)
        CarStatusPill(state: .paused, style: .compact)
        CarStatusPill(state: .sold, style: .compact)
    }
    .padding()
}

#Preview("Hero") {
    VStack(alignment: .leading, spacing: 8) {
        CarStatusPill(state: .available, style: .hero)
        CarStatusPill(state: .rented(nil), style: .hero)
        CarStatusPill(state: .awaitingApproval, style: .hero)
        CarStatusPill(state: .paused, style: .hero)
        CarStatusPill(state: .sold, style: .hero)
    }
    .padding()
    .background(Color.black)
}
