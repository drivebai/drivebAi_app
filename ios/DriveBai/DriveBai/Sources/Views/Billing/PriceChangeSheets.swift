import SwiftUI

// Mid-rental price change: offer → the driver accepts → it applies from the
// NEXT cycle.
//
// It cannot be unilateral, and that is not a policy choice. The driver's
// authorization records one specific amount. Charging a different one without
// a fresh agreement is an unauthorized charge, which auto-loses as a dispute
// and costs the chargeback fee on top. So the owner proposes and the driver
// agrees, or nothing changes.

// MARK: - Owner: propose

struct ProposePriceChangeSheet: View {
    let leaseRequestId: UUID
    let currentAmountCents: Int64
    let currencyCode: String
    let onProposed: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var amountText: String = ""
    @State private var isSending = false
    @State private var errorMessage: String?

    private var newCents: Int64? {
        let cleaned = amountText.replacingOccurrences(of: ",", with: ".")
            .trimmingCharacters(in: .whitespaces)
        guard let value = Double(cleaned), value > 0 else { return nil }
        return Int64((value * 100).rounded())
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Current price")
                            .font(.subheadline).foregroundColor(.secondary)
                        Text(Self.money(currentAmountCents, currencyCode) + " per week")
                            .font(.title3.weight(.semibold))
                    }

                    VStack(alignment: .leading, spacing: 8) {
                        Text("New weekly price")
                            .font(.subheadline.weight(.semibold))
                        TextField("0.00", text: $amountText)
                            .keyboardType(.decimalPad)
                            .textFieldStyle(.roundedBorder)
                            .font(.title3)
                    }

                    Text("Your driver has to accept this before anything changes. If they accept, the new price starts from their next billing week — the week they've already paid for is never re-charged. If they don't accept, the offer expires and the current price stands.")
                        .font(.footnote)
                        .foregroundColor(.secondary)
                        .fixedSize(horizontal: false, vertical: true)

                    if let newCents, newCents != currentAmountCents {
                        let up = newCents > currentAmountCents
                        Label(
                            up ? "That's an increase of \(Self.money(newCents - currentAmountCents, currencyCode)) a week"
                               : "That's a reduction of \(Self.money(currentAmountCents - newCents, currencyCode)) a week",
                            systemImage: up ? "arrow.up.circle" : "arrow.down.circle")
                            .font(.subheadline)
                            .foregroundColor(up ? .orange : .green)
                    }
                }
                .padding(20)
            }
            .navigationTitle("Change the price")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            }
            .safeAreaInset(edge: .bottom) {
                Button {
                    Task { await send() }
                } label: {
                    HStack(spacing: 6) {
                        if isSending { ProgressView().tint(.white).scaleEffect(0.8) }
                        Text("Send offer to driver").font(.headline)
                    }
                    .foregroundColor(.white)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 14)
                    .background(newCents != nil && newCents != currentAmountCents
                                ? Color.driveBaiPrimary : Color(.systemGray3))
                    .clipShape(RoundedRectangle(cornerRadius: 12))
                }
                .disabled(newCents == nil || newCents == currentAmountCents || isSending)
                .padding(.horizontal, 20)
                .padding(.bottom, 8)
                .background(.bar)
            }
            .alert("Couldn't send the offer", isPresented: Binding(
                get: { errorMessage != nil }, set: { if !$0 { errorMessage = nil } })) {
                Button("OK") { errorMessage = nil }
            } message: { Text(errorMessage ?? "") }
        }
    }

    private func send() async {
        guard let cents = newCents else { return }
        isSending = true
        defer { isSending = false }
        do {
            _ = try await APIClient.shared.proposeAmendment(leaseRequestId: leaseRequestId, newAmountCents: cents)
            onProposed()
            dismiss()
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    static func money(_ cents: Int64, _ code: String) -> String {
        let f = NumberFormatter()
        f.numberStyle = .currency
        f.currencyCode = code.isEmpty ? "USD" : code
        let v = Double(cents) / 100
        return f.string(from: NSNumber(value: v)) ?? String(format: "$%.2f", v)
    }
}

// MARK: - Driver: review

struct ReviewPriceChangeSheet: View {
    let pending: PendingAmendmentAPIModel
    let currentAmountCents: Int64
    let currencyCode: String
    let onActed: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var isActing = false
    @State private var errorMessage: String?

    private var isIncrease: Bool { pending.offer.newAmountCents > currentAmountCents }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    HStack(alignment: .firstTextBaseline, spacing: 12) {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Now").font(.caption).foregroundColor(.secondary)
                            Text(ProposePriceChangeSheet.money(currentAmountCents, currencyCode))
                                .font(.title3).strikethrough()
                        }
                        Image(systemName: "arrow.right").foregroundColor(.secondary)
                        VStack(alignment: .leading, spacing: 4) {
                            Text("From your next week").font(.caption).foregroundColor(.secondary)
                            Text(ProposePriceChangeSheet.money(pending.offer.newAmountCents, currencyCode))
                                .font(.title2.weight(.bold))
                                .foregroundColor(isIncrease ? .orange : .green)
                        }
                    }

                    // The EXACT text acceptance records, served by the server.
                    // Never a local copy: the stored row is the evidence.
                    if let preview = pending.disclosurePreview, !preview.isEmpty {
                        VStack(alignment: .leading, spacing: 8) {
                            Text("What you're agreeing to")
                                .font(.subheadline.weight(.semibold))
                            Text(preview)
                                .font(.footnote)
                                .fixedSize(horizontal: false, vertical: true)
                                .textSelection(.enabled)
                                .padding(14)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(Color(.secondarySystemGroupedBackground))
                                .overlay(RoundedRectangle(cornerRadius: 12).stroke(Color(.systemGray4), lineWidth: 1))
                                .clipShape(RoundedRectangle(cornerRadius: 12))
                        }
                    }

                    Text("Nothing is charged now, and the week you've already paid for doesn't change. If you decline, or do nothing, your current price stays as it is.")
                        .font(.footnote)
                        .foregroundColor(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(20)
            }
            .background(Color(.systemGroupedBackground))
            .navigationTitle(isIncrease ? "Price increase" : "Price reduction")
            .navigationBarTitleDisplayMode(.inline)
            .safeAreaInset(edge: .bottom) {
                VStack(spacing: 8) {
                    Button { Task { await act(accept: true) } } label: {
                        Text("Accept from next week").font(.headline)
                            .foregroundColor(.white).frame(maxWidth: .infinity)
                            .padding(.vertical, 14)
                            .background(Color.driveBaiPrimary)
                            .clipShape(RoundedRectangle(cornerRadius: 12))
                    }
                    .disabled(isActing)
                    Button { Task { await act(accept: false) } } label: {
                        Text("Decline").font(.subheadline.weight(.semibold))
                            .frame(maxWidth: .infinity).padding(.vertical, 12)
                    }
                    .disabled(isActing)
                }
                .padding(.horizontal, 20).padding(.bottom, 8).background(.bar)
            }
            .alert("Couldn't do that", isPresented: Binding(
                get: { errorMessage != nil }, set: { if !$0 { errorMessage = nil } })) {
                Button("OK") { errorMessage = nil }
            } message: { Text(errorMessage ?? "") }
        }
    }

    private func act(accept: Bool) async {
        isActing = true
        defer { isActing = false }
        do {
            if accept {
                _ = try await APIClient.shared.acceptAmendment(amendmentId: pending.offer.id)
            } else {
                _ = try await APIClient.shared.declineAmendment(amendmentId: pending.offer.id)
            }
            onActed()
            dismiss()
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}
