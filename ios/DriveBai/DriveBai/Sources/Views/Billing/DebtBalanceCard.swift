import SwiftUI
import StripePaymentSheet

// The driver's outstanding balance, the way a ride-hailing app shows one:
// a single number, unmissable, payable in one tap.
//
// This is deliberately NOT a per-rental arrears card. A debt outlives the
// rental that created it and sums across rentals, so the person sees what
// they owe, not what one lease is doing.

@MainActor
final class DebtBalanceViewModel: ObservableObject {
    @Published private(set) var balance: DriverBalanceAPIResponse?
    @Published private(set) var isLoading = false
    @Published private(set) var isPaying = false
    @Published var message: String?
    @Published var pendingPayment: PaymentIntentAPIResponse?

    private let apiClient: APIClientProtocol

    init(apiClient: APIClientProtocol = APIClient.shared) {
        self.apiClient = apiClient
    }

    var hasBalance: Bool { balance?.hasBalance == true }

    func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            balance = try await apiClient.fetchMyBalance()
        } catch {
            // A failed read must not invent a balance of zero — that would
            // hide a real debt. Leave the last known value and stay quiet.
            if balance == nil { balance = nil }
        }
    }

    /// Clears the balance oldest-debt-first, which is the order a person
    /// expects and the order the server settles in.
    func payOldest() async {
        guard !isPaying, let debt = balance?.oldestOpenDebt else { return }
        isPaying = true
        defer { isPaying = false }
        do {
            let intent = try await apiClient.payNowForBilling(leaseRequestId: debt.leaseRequestId)
            guard let customerId = intent.customerId, !customerId.isEmpty,
                  let key = intent.ephemeralKeySecret, !key.isEmpty else {
                message = "We couldn't open the payment form. Please contact support so we can take this payment."
                return
            }
            pendingPayment = intent
        } catch let apiError as APIError {
            switch apiError.errorCode {
            case RollingBillingErrorCode.alreadyPaid, RollingBillingErrorCode.nothingDue:
                message = apiError.errorDescription
                await load()
            default:
                message = apiError.errorDescription
            }
        } catch {
            message = "Couldn't start the payment. Please try again."
        }
    }

    func handleResult(_ result: PaymentSheetResult) async {
        pendingPayment = nil
        switch result {
        case .completed:
            message = "Payment received. It can take a moment to show here."
        case .canceled:
            break
        case .failed(let error):
            message = "Payment failed: \(error.localizedDescription)"
        }
        await load()
    }
}

struct DebtBalanceCard: View {
    @StateObject private var viewModel = DebtBalanceViewModel()
    var onChanged: () -> Void = {}

    var body: some View {
        Group {
            if viewModel.hasBalance, let balance = viewModel.balance {
                content(balance)
            } else {
                EmptyView()
            }
        }
        .task { await viewModel.load() }
        .background { paymentHost }
        .alert("Balance", isPresented: Binding(
            get: { viewModel.message != nil },
            set: { if !$0 { viewModel.message = nil } }
        )) {
            Button("OK") { viewModel.message = nil }
        } message: {
            Text(viewModel.message ?? "")
        }
    }

    private func content(_ balance: DriverBalanceAPIResponse) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                Image(systemName: "exclamationmark.circle.fill")
                    .foregroundColor(.red)
                Text("You owe \(balance.formatted)")
                    .font(.system(size: 17, weight: .semibold))
                Spacer()
            }

            Text(balance.openDebtCount > 1
                 ? "From \(balance.openDebtCount) rentals where a payment couldn't be collected. You can't start a new rental until this is cleared."
                 : "From days you used on a rental where a payment couldn't be collected. You can't start a new rental until this is cleared.")
                .font(.subheadline)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            Button {
                Task {
                    await viewModel.payOldest()
                    onChanged()
                }
            } label: {
                HStack(spacing: 6) {
                    if viewModel.isPaying { ProgressView().tint(.white).scaleEffect(0.8) }
                    Text("Pay \(balance.formatted)")
                        .font(.system(size: 15, weight: .semibold))
                }
                .foregroundColor(.white)
                .frame(maxWidth: .infinity)
                .frame(height: 44)
                .background(Color.red)
                .cornerRadius(10)
            }
            .disabled(viewModel.isPaying)
        }
        .padding(16)
        .background(TodayLayout.cardBackgroundColor)
        .cornerRadius(TodayLayout.cardCornerRadius)
        .overlay(
            RoundedRectangle(cornerRadius: TodayLayout.cardCornerRadius)
                .stroke(Color.red.opacity(0.45), lineWidth: 1)
        )
    }

    @ViewBuilder
    private var paymentHost: some View {
        if let intent = viewModel.pendingPayment,
           let customerId = intent.customerId, let key = intent.ephemeralKeySecret {
            PaymentSheetPresenter(
                clientSecret: intent.paymentIntentClientSecret,
                ephemeralKeySecret: key,
                customerId: customerId,
                publishableKey: intent.publishableKey
            ) { result in
                Task {
                    await viewModel.handleResult(result)
                    onChanged()
                }
            }
            .frame(width: 0, height: 0)
            .id(intent.paymentIntentId)
        }
    }
}
