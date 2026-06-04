import SwiftUI

struct KeyHandoverDetailView: View {
    @StateObject private var vm: KeyHandoverDetailViewModel

    init(handover: KeyHandover) {
        _vm = StateObject(wrappedValue: KeyHandoverDetailViewModel(handover: handover))
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                statusHeader
                pickupCard
                counterpartyCard
                timelineCard
                if let cta = vm.handover.primaryActionTitle {
                    confirmButton(title: cta)
                }
            }
            .padding(16)
        }
        .background(Color(.systemGroupedBackground).ignoresSafeArea())
        .navigationTitle("Key handover")
        .navigationBarTitleDisplayMode(.inline)
        .task { await vm.reload() }
        .alert("Error", isPresented: Binding(
            get: { vm.error != nil },
            set: { if !$0 { vm.error = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: { Text(vm.error ?? "") }
    }

    // MARK: Status header

    private var statusHeader: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 10) {
                ZStack {
                    Circle().fill(Color.driveBaiPrimary.opacity(0.12)).frame(width: 46, height: 46)
                    Image(systemName: "key.fill")
                        .font(.system(size: 18, weight: .semibold))
                        .foregroundColor(.driveBaiPrimary)
                }
                VStack(alignment: .leading, spacing: 2) {
                    if !vm.handover.carTitle.isEmpty {
                        Text(vm.handover.carTitle).font(.headline)
                    }
                    Text(statusLabel)
                        .font(.caption.weight(.semibold))
                        .foregroundColor(statusColor)
                }
                Spacer()
            }

            Text(vm.handover.statusMessage)
                .font(.subheadline)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            if vm.handover.showsCountdown, let deadline = vm.handover.confirmationDeadline {
                countdownRow(deadline: deadline)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .cardSurface()
    }

    private func countdownRow(deadline: Date) -> some View {
        let remaining = max(0, deadline.timeIntervalSince(vm.currentTime))
        let expired = remaining <= 0
        return Label(
            expired ? "Confirmation window closed" : "Confirm within \(Self.countdownString(remaining))",
            systemImage: expired ? "exclamationmark.triangle.fill" : "clock.fill"
        )
        .font(.subheadline.weight(.medium))
        .foregroundColor(expired ? Color.driveBaiSecondary : .driveBaiPrimary)
    }

    // MARK: Pickup

    private var pickupCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Pickup location")
                .font(.subheadline.weight(.semibold))
            Label(vm.handover.pickupLocationText, systemImage: "mappin.and.ellipse")
                .font(.subheadline)
                .foregroundColor(.secondary)
            if let lat = vm.handover.pickupLatitude, let lng = vm.handover.pickupLongitude,
               let url = URL(string: "http://maps.apple.com/?ll=\(lat),\(lng)") {
                Link(destination: url) {
                    Label("Open in Maps", systemImage: "map.fill")
                        .font(.subheadline.weight(.semibold))
                        .foregroundColor(.driveBaiPrimary)
                }
                .padding(.top, 2)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .cardSurface()
    }

    // MARK: Counterparty

    private var counterpartyCard: some View {
        HStack(spacing: 12) {
            ZStack {
                Circle().fill(Color(.systemGray5)).frame(width: 40, height: 40)
                Image(systemName: "person.fill").foregroundColor(.gray)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(vm.handover.counterpartyName.isEmpty ? "Counterparty" : vm.handover.counterpartyName)
                    .font(.subheadline.weight(.semibold))
                Text(vm.handover.isOwner ? "Driver" : "Car owner")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            Spacer()
        }
        .padding(16)
        .cardSurface()
    }

    // MARK: Timeline

    private var timelineCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Progress")
                .font(.subheadline.weight(.semibold))
            timelineRow(done: true, label: "Payment received", date: vm.handover.createdAt)
            timelineRow(done: vm.handover.ownerConfirmedAt != nil,
                        label: "Owner handed over keys", date: vm.handover.ownerConfirmedAt)
            timelineRow(done: vm.handover.status == .completed,
                        label: "Driver confirmed receipt", date: vm.handover.driverConfirmedAt,
                        isLast: vm.handover.status != .expired)
            if vm.handover.status == .expired {
                timelineRow(done: true, label: "Handover expired", date: nil, isLast: true, isError: true)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .cardSurface()
    }

    private func timelineRow(done: Bool, label: String, date: Date?, isLast: Bool = false, isError: Bool = false) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: isError ? "xmark.circle.fill" : (done ? "checkmark.circle.fill" : "circle"))
                .font(.system(size: 16))
                .foregroundColor(isError ? Color.driveBaiSecondary : (done ? .driveBaiPrimary : Color(.systemGray3)))
            VStack(alignment: .leading, spacing: 2) {
                Text(label)
                    .font(.subheadline)
                    .foregroundColor(done ? .primary : .secondary)
                if let date {
                    Text(Self.dateFormatter.string(from: date))
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
            }
            Spacer()
        }
    }

    // MARK: CTA

    private func confirmButton(title: String) -> some View {
        Button(action: { vm.confirm() }) {
            HStack(spacing: 8) {
                if vm.isSubmitting { ProgressView().tint(.white).scaleEffect(0.85) }
                Text(vm.isSubmitting ? "Confirming…" : title)
                    .font(.subheadline.weight(.semibold))
            }
            .frame(maxWidth: .infinity, minHeight: 52)
            .background(vm.isSubmitting ? Color.driveBaiPrimary.opacity(0.6) : Color.driveBaiPrimary)
            .foregroundColor(.white)
            .cornerRadius(14)
        }
        .disabled(vm.isSubmitting)
    }

    // MARK: Status styling

    private var statusLabel: String {
        switch vm.handover.status {
        case .pending:        return "Pending"
        case .ownerConfirmed: return "Awaiting driver confirmation"
        case .completed:      return "Completed"
        case .expired:        return "Expired"
        }
    }

    private var statusColor: Color {
        switch vm.handover.status {
        case .pending:        return .driveBaiPrimary
        case .ownerConfirmed: return .orange
        case .completed:      return .green
        case .expired:        return Color.driveBaiSecondary
        }
    }

    private static func countdownString(_ seconds: TimeInterval) -> String {
        let total = Int(seconds)
        let m = total / 60
        let s = total % 60
        return String(format: "%d:%02d", m, s)
    }

    private static let dateFormatter: DateFormatter = {
        let f = DateFormatter()
        f.dateStyle = .medium
        f.timeStyle = .short
        return f
    }()
}

// MARK: - Card surface helper

private extension View {
    func cardSurface() -> some View {
        self
            .background(Color(.systemBackground))
            .cornerRadius(14)
            .overlay(
                RoundedRectangle(cornerRadius: 14)
                    .stroke(Color(.systemGray5), lineWidth: 1)
            )
    }
}
