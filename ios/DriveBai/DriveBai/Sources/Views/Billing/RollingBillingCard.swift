import SwiftUI
import StripePaymentSheet

// The driver's weekly-billing surface: what is about to be charged, what
// went wrong if something did, and the exits. Every state the engine can
// leave a lease in has a visible next step here — a driver blocked on
// bank authentication, on a dead card, or on a balance after returning
// the car must never see a screen with nothing to press.

// MARK: - View model

@MainActor
final class RollingBillingViewModel: ObservableObject {
    /// nil until the first load succeeds; the card renders nothing until then.
    @Published private(set) var status: BillingStatusAPIResponse?
    /// True once the server tells us this lease isn't rolling — the card
    /// removes itself rather than polling an endpoint that will keep 409ing.
    @Published private(set) var notApplicable = false
    @Published private(set) var isLoading = false
    @Published private(set) var isActing = false
    /// The last load attempt failed. Drives the retry row rather than
    /// letting the whole card vanish.
    @Published private(set) var loadFailed = false
    /// One channel, so two things that happen at once cannot each claim
    /// the alert and leave the loser stuck behind a stale binding.
    @Published var message: String?
    var errorMessage: String? {
        get { message }
        set { message = newValue }
    }
    var infoMessage: String? {
        get { message }
        set { message = newValue }
    }

    func clearMessage() { message = nil }
    /// Set to drive PaymentSheet (payment mode); cleared when it closes.
    @Published var pendingPayment: PaymentIntentAPIResponse?
    /// Set to drive PaymentSheet (setup mode) for a card replacement.
    @Published var pendingCardUpdate: CardUpdateStartAPIResponse?

    let leaseRequestId: UUID
    private let apiClient: APIClientProtocol

    init(leaseRequestId: UUID, apiClient: APIClientProtocol = APIClient.shared) {
        self.leaseRequestId = leaseRequestId
        self.apiClient = apiClient
    }

    // MARK: Loading

    func load() async {
        guard !notApplicable else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            let fresh = try await apiClient.fetchBillingStatus(leaseRequestId: leaseRequestId)
            guard fresh.isRolling else {
                notApplicable = true
                status = nil
                loadFailed = false
                return
            }
            status = fresh
            loadFailed = false
            // A stale error must not outlive the failure that caused it.
            errorMessage = nil
        } catch let apiError as APIError {
            if apiError.errorCode == RollingBillingErrorCode.notRolling {
                notApplicable = true
                status = nil
                loadFailed = false
                return
            }
            if Self.isCancellation(apiError) { return }
            // A failed refresh must not blank a card that is already
            // showing a real state — keep the last good status and record
            // that the last attempt failed so the card can offer a retry.
            loadFailed = true
            if status == nil {
                // Nothing on screen yet: the card renders its own retry row,
                // so an alert on top of it would just be noise.
                return
            }
            errorMessage = apiError.errorDescription
        } catch {
            if Self.isCancellation(error) { return }
            loadFailed = true
            if status != nil {
                errorMessage = "Couldn't refresh your weekly billing."
            }
        }
    }

    /// A view going away cancels its own request. That is not a billing
    /// problem and must never be reported to the driver as one.
    private static func isCancellation(_ error: Error) -> Bool {
        if error is CancellationError { return true }
        if let urlError = error as? URLError, urlError.code == .cancelled { return true }
        if case APIError.networkError(let inner) = error {
            if inner is CancellationError { return true }
            if let urlError = inner as? URLError, urlError.code == .cancelled { return true }
        }
        return false
    }

    // MARK: Actions

    /// Both the bank-authentication rescue and the failed-payment retry go
    /// through pay-now: the server hands back the cycle's OWN intent, so a
    /// success settles the week through the normal path instead of
    /// creating a second charge for the same week.
    func payNow() async {
        guard !isActing else { return }
        isActing = true
        defer { isActing = false }
        do {
            let intent = try await apiClient.payNowForBilling(leaseRequestId: leaseRequestId)
            guard let customerId = intent.customerId, !customerId.isEmpty,
                  let ephemeralKey = intent.ephemeralKeySecret, !ephemeralKey.isEmpty else {
                // Never fail silently: without these the card form cannot
                // open, and a driver would be left tapping a dead button.
                errorMessage = "We couldn't open the payment form. Please contact support so we can take this payment."
                return
            }
            pendingPayment = nil
            pendingPayment = intent
        } catch let apiError as APIError {
            switch apiError.errorCode {
            case RollingBillingErrorCode.alreadyPaid, RollingBillingErrorCode.nothingDue:
                infoMessage = apiError.errorDescription
                await load()
            default:
                errorMessage = apiError.errorDescription
            }
        } catch {
            errorMessage = "Couldn't start the payment. Please try again."
        }
    }

    func handlePaymentResult(_ result: PaymentSheetResult) async {
        pendingPayment = nil
        switch result {
        case .completed:
            infoMessage = "Payment received. It can take a moment to show here."
            await load()
        case .canceled:
            await load()
        case .failed(let error):
            errorMessage = "Payment failed: \(error.localizedDescription)"
            await load()
        }
    }

    func startCardUpdate() async {
        guard !isActing else { return }
        isActing = true
        defer { isActing = false }
        do {
            let start = try await apiClient.startCardUpdate(leaseRequestId: leaseRequestId)
            guard let customerId = start.customerId, !customerId.isEmpty,
                  let ephemeralKey = start.ephemeralKeySecret, !ephemeralKey.isEmpty else {
                errorMessage = "We couldn't open the card form. Please contact support."
                return
            }
            pendingCardUpdate = nil
            pendingCardUpdate = start
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription
        } catch {
            errorMessage = "Couldn't start the card update. Please try again."
        }
    }

    func handleCardUpdateResult(_ result: PaymentSheetResult, setupIntentId: String) async {
        pendingCardUpdate = nil
        switch result {
        case .completed:
            // Saving the card at Stripe is only half of it: until the
            // server verifies that SetupIntent and swaps the mandate, the
            // weekly charge still points at the old card.
            do {
                let done = try await apiClient.completeCardUpdate(leaseRequestId: leaseRequestId,
                                                                  setupIntentId: setupIntentId)
                if let brand = done.cardBrand, let last4 = done.cardLast4, !last4.isEmpty {
                    infoMessage = "Weekly payments now use your \(brand.capitalized) ending \(last4)."
                } else {
                    infoMessage = "Your card was updated."
                }
            } catch let apiError as APIError {
                errorMessage = apiError.errorDescription
            } catch {
                errorMessage = "Your card was saved but we couldn't finish the update. Please try again."
            }
            await load()
        case .canceled:
            break
        case .failed(let error):
            errorMessage = "Card update failed: \(error.localizedDescription)"
        }
    }

    func withdrawAmendment(_ id: UUID) async {
        guard !isActing else { return }
        isActing = true
        defer { isActing = false }
        do {
            _ = try await apiClient.withdrawAmendment(amendmentId: id)
            message = "Your price change offer was withdrawn."
        } catch let apiError as APIError {
            message = apiError.errorDescription
        } catch {
            message = "Couldn't withdraw the offer. Please try again."
        }
        await load()
    }

    func stopRenewal() async {
        guard !isActing else { return }
        isActing = true
        defer { isActing = false }
        do {
            _ = try await apiClient.stopRenewal(leaseRequestId: leaseRequestId)
            infoMessage = "Auto-renew is off. Your rental ends when the week you've paid for runs out."
        } catch let apiError as APIError {
            if apiError.errorCode == RollingBillingErrorCode.renewalStopRefused {
                infoMessage = apiError.errorDescription
            } else {
                errorMessage = apiError.errorDescription
            }
        } catch {
            errorMessage = "Couldn't turn off auto-renew. Please try again."
        }
        await load()
    }
}

// MARK: - Card

struct RollingBillingCard: View {
    @StateObject private var viewModel: RollingBillingViewModel
    /// A return handshake is in flight. The server refuses every payment
    /// action while one is open (pay-now's RETURN_IN_PROGRESS guard checks
    /// the returns table, not just the halt slot), so the card must not
    /// offer buttons that can only 409. Both hosts already track this.
    var hasOpenReturn: Bool = false
    /// True when the viewer OWNS the car. The owner proposes a price change;
    /// the driver reviews it. Same card, two sides of one negotiation.
    var isOwner: Bool = false
    /// Called after any action that can change the rental itself, so the
    /// host screen can re-read the lease it is showing beside this card.
    var onChanged: () -> Void = {}

    @State private var showStopRenewalConfirm = false
    @State private var showProposePrice = false
    @State private var showReviewPrice = false
    @Environment(\.scenePhase) private var scenePhase

    init(leaseRequestId: UUID, hasOpenReturn: Bool = false, isOwner: Bool = false,
         onChanged: @escaping () -> Void = {}) {
        _viewModel = StateObject(wrappedValue: RollingBillingViewModel(leaseRequestId: leaseRequestId))
        self.hasOpenReturn = hasOpenReturn
        self.isOwner = isOwner
        self.onChanged = onChanged
    }

    /// What the card is asking the driver to do, most urgent first. Money
    /// owed outranks information; a blocked payment outranks a healthy one.
    private enum Banner: Equatable {
        case arrears(cents: Int64)
        case authenticate(cents: Int64)
        /// `final` = the retry ladder is finished. The difference matters:
        /// one says wait, the other says act or the rental ends.
        case paymentFailed(cents: Int64, final: Bool)
        /// A charge is uncollected but the driver cannot act on it right
        /// now — the engine is retrying, or the rental is stopping. Stating
        /// it without a button beats a button the server will refuse.
        case collectionPending
        case cardProblem
        /// A mandate exists but was never activated with a usable card.
        case needsCard
        case paused(reason: String)
        case renewalStopped
        case healthy
    }

    /// The server hands out a payable secret only on a live lease that
    /// nobody has stopped and no return is running (rolling_driver.go's
    /// pay-now guards: RETURN_IN_PROGRESS, RENEWALS_STOPPED). Offering a
    /// button outside that window produces a 409, not a payment.
    private var payableCycle: BillingOpenCycleAPIModel? {
        guard let status = viewModel.status, let cycle = status.openCycle else { return nil }
        guard !status.isReturned, !hasOpenReturn,
              status.renewalStoppedAt == nil,
              status.renewalHaltedReason != "return_initiated" else { return nil }
        return (cycle.needsAuthentication || cycle.isUncollected) ? cycle : nil
    }

    private var banner: Banner {
        guard let status = viewModel.status else { return .healthy }
        // Money owed after the car is back is the only thing that matters.
        if let arrears = status.arrears {
            return .arrears(cents: arrears.amountCents)
        }
        if let cycle = payableCycle {
            if cycle.needsAuthentication { return .authenticate(cents: cycle.amountCents) }
            return .paymentFailed(cents: cycle.amountCents, final: cycle.status == "failed_final")
        }
        if let reason = status.renewalHaltedReason {
            if reason == "consent_revoked" { return .cardProblem }
            return .paused(reason: reason)
        }
        // A consent row that never activated has no chargeable card behind
        // it — silently rendering that as healthy would let the rental run
        // to its first failed charge before anyone noticed.
        if status.consentActive == false && !status.isReturned {
            return .needsCard
        }
        if status.renewalStoppedAt != nil { return .renewalStopped }
        // Delinquent with nothing the driver can pay: the retry ladder owns
        // it, so say so instead of showing a dead button.
        if status.delinquentSince != nil || status.openCycle?.isUncollected == true {
            return .collectionPending
        }
        return .healthy
    }

    /// After the car is back and nothing is owed, this card has nothing
    /// left to say — the return flow owns the rest of the story.
    private var isFinished: Bool {
        guard let status = viewModel.status else { return false }
        return status.isReturned && status.arrears == nil
    }

    var body: some View {
        Group {
            if viewModel.notApplicable || isFinished {
                EmptyView()
            } else if viewModel.status != nil {
                content
            } else if viewModel.loadFailed {
                // Never silently disappear: a driver whose weekly billing
                // failed to load still has money at stake and needs a way
                // back to it.
                retryRow
            } else {
                loadingRow
            }
        }
        .task { await viewModel.load() }
        .onChange(of: scenePhase) { _, phase in
            // Coming back from the bank's 3DS screen, or from anywhere
            // else, is exactly when this card is most likely to be stale.
            if phase == .active {
                Task { await viewModel.load() }
            }
        }
        .background { paymentSheetHost }
        .background { cardUpdateSheetHost }
        .alert("Weekly billing", isPresented: alertBinding(for: $viewModel.message)) {
            Button("OK") { viewModel.clearMessage() }
        } message: {
            Text(viewModel.message ?? "")
        }
        .confirmationDialog("Turn off auto-renew?",
                            isPresented: $showStopRenewalConfirm,
                            titleVisibility: .visible) {
            Button("Turn off auto-renew", role: .destructive) {
                Task {
                    await viewModel.stopRenewal()
                    onChanged()
                }
            }
            Button("Keep it on", role: .cancel) {}
        } message: {
            Text("You keep the car until the week you've already paid for runs out. No further weekly charges will be made. Returning the car earlier stops the charges too.")
        }
    }

    // MARK: Sections

    private var content: some View {
        VStack(alignment: .leading, spacing: 12) {
            headerRow
            bannerView
            // The schedule and the card on file stay visible in every state
            // that isn't asking for money right now — they are the context
            // a driver checks between charges.
            // The paid-through date stays visible even while money is
            // owed: "when does this end if I do nothing" is exactly what a
            // driver in that state needs, and hiding it was how the
            // terminal case became unreadable.
            if !isArrearsBanner && banner != .cardProblem && banner != .needsCard {
                scheduleLines
            }
            priceChangeSection
            actionButtons
        }
        .padding(16)
        .background(TodayLayout.cardBackgroundColor)
        .cornerRadius(TodayLayout.cardCornerRadius)
        .overlay(
            RoundedRectangle(cornerRadius: TodayLayout.cardCornerRadius)
                .stroke(accentColor.opacity(needsAttention ? 0.45 : 0.0001), lineWidth: 1)
        )
        .overlay(
            RoundedRectangle(cornerRadius: TodayLayout.cardCornerRadius)
                .stroke(TodayLayout.cardBorderColor, lineWidth: needsAttention ? 0 : TodayLayout.cardBorderWidth)
        )
    }

    private var retryRow: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                Text("Weekly billing")
                    .font(.system(size: 15, weight: .semibold))
                Text("We couldn't load it just now.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            Spacer()
            Button("Retry") {
                Task { await viewModel.load() }
            }
            .font(.system(size: 14, weight: .semibold))
        }
        .padding(16)
        .background(TodayLayout.cardBackgroundColor)
        .cornerRadius(TodayLayout.cardCornerRadius)
        .overlay(
            RoundedRectangle(cornerRadius: TodayLayout.cardCornerRadius)
                .stroke(TodayLayout.cardBorderColor, lineWidth: TodayLayout.cardBorderWidth)
        )
    }

    private var loadingRow: some View {
        HStack(spacing: 8) {
            ProgressView().scaleEffect(0.8)
            Text("Loading weekly billing…")
                .font(.caption)
                .foregroundColor(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .background(TodayLayout.cardBackgroundColor)
        .cornerRadius(TodayLayout.cardCornerRadius)
        .overlay(
            RoundedRectangle(cornerRadius: TodayLayout.cardCornerRadius)
                .stroke(TodayLayout.cardBorderColor, lineWidth: TodayLayout.cardBorderWidth)
        )
    }

    private var headerRow: some View {
        HStack(spacing: 8) {
            Image(systemName: needsAttention ? "exclamationmark.circle.fill" : "arrow.triangle.2.circlepath")
                .foregroundColor(needsAttention ? accentColor : TodayLayout.tealAccent)
            Text("Weekly billing")
                .font(.system(size: 16, weight: .semibold))
            Spacer()
            if let weekly = viewModel.status?.amountCents {
                Text("\(Self.money(weekly))/week")
                    .font(.subheadline.weight(.semibold))
                    .monospacedDigit()
            }
        }
    }

    @ViewBuilder
    private var bannerView: some View {
        switch banner {
        case .arrears(let cents):
            bannerBody(
                title: "You owe \(Self.money(cents)) for days you used",
                detail: "This covers the days you had the car in your last billing week, which were never paid for. Paying now closes out the rental.",
                tone: .red
            )
        case .authenticate(let cents):
            bannerBody(
                title: "Your bank needs you to approve \(Self.money(cents))",
                detail: "This week's payment is waiting on a confirmation from your bank. Approve it to keep the car.",
                tone: .orange
            )
        case .paymentFailed(let cents, let final):
            bannerBody(
                title: "We couldn't collect \(Self.money(cents))",
                detail: final
                    ? "We've stopped retrying your card. Pay now to keep the rental going — otherwise it ends when the time you've paid for runs out."
                    : "We'll keep retrying your card for two days and you keep the car meanwhile. Paying now settles it immediately.",
                tone: .red
            )
        case .collectionPending:
            bannerBody(
                title: "A weekly payment didn't go through",
                detail: "We're retrying your card and you keep the car meanwhile. There's nothing for you to do right now — we'll message you if we need a new card.",
                tone: .orange
            )
        case .needsCard:
            bannerBody(
                title: "We don't have a card saved for your weekly payments",
                detail: "Add one now so next week's charge can go through. Without it, weekly billing stops and your rental ends when your paid time runs out.",
                tone: .orange
            )
        case .cardProblem:
            bannerBody(
                title: "We can't charge your saved card",
                detail: "Weekly billing is paused until you add a working card. Your rental ends when your paid time runs out if it isn't fixed.",
                tone: .red
            )
        case .paused(let reason):
            bannerBody(title: Self.pausedTitle(reason), detail: Self.pausedDetail(reason), tone: .orange)
        case .renewalStopped:
            bannerBody(
                title: "Auto-renew is off",
                detail: endsLine ?? "Your rental ends when the week you've paid for runs out.",
                tone: .secondaryTone
            )
        case .healthy:
            EmptyView()
        }
    }

    private var scheduleLines: some View {
        VStack(alignment: .leading, spacing: 6) {
            if banner == .healthy, let next = viewModel.status?.nextChargeAt {
                Label("Next charge \(Self.day(next))", systemImage: "calendar")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            if let ends = endsLine, banner != .renewalStopped {
                Label(ends, systemImage: "clock")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            if let brand = viewModel.status?.cardBrand, let last4 = viewModel.status?.cardLast4,
               !brand.isEmpty, !last4.isEmpty {
                Label("\(brand.capitalized) •••• \(last4)", systemImage: "creditcard")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
        }
    }

    /// A price change in flight, from whichever side the viewer is on. An
    /// offer nobody actions simply expires and the current price stands —
    /// that is an honest resting state, so the copy says so.
    @ViewBuilder
    private var priceChangeSection: some View {
        if let pending = viewModel.status?.pendingAmendment {
            VStack(alignment: .leading, spacing: 8) {
                Divider()
                if isOwner {
                    Label("Price change sent — waiting for your driver",
                          systemImage: "hourglass")
                        .font(.subheadline.weight(.semibold))
                    Text("They have to accept before anything changes. If they don't, your current price stays as it is.")
                        .font(.caption).foregroundColor(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                    Button("Withdraw the offer") {
                        Task {
                            await viewModel.withdrawAmendment(pending.offer.id)
                            onChanged()
                        }
                    }
                    .font(.system(size: 14, weight: .semibold))
                } else {
                    Label("Your owner has proposed a new weekly price",
                          systemImage: "arrow.left.arrow.right.circle")
                        .font(.subheadline.weight(.semibold))
                    Text("Nothing changes until you agree, and the week you've paid for is never re-charged.")
                        .font(.caption).foregroundColor(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                    Button("Review the new price") { showReviewPrice = true }
                        .font(.system(size: 14, weight: .semibold))
                }
            }
            .sheet(isPresented: $showReviewPrice) {
                ReviewPriceChangeSheet(
                    pending: pending,
                    currentAmountCents: viewModel.status?.amountCents ?? 0,
                    currencyCode: "USD"
                ) {
                    Task { await viewModel.load(); onChanged() }
                }
            }
        } else if isOwner, viewModel.status?.renewalsRunning == true,
                  let amount = viewModel.status?.amountCents {
            Divider()
            Button("Change the weekly price") { showProposePrice = true }
                .font(.system(size: 14, weight: .semibold))
                .sheet(isPresented: $showProposePrice) {
                    ProposePriceChangeSheet(
                        leaseRequestId: viewModel.leaseRequestId,
                        currentAmountCents: amount,
                        currencyCode: "USD"
                    ) {
                        Task { await viewModel.load(); onChanged() }
                    }
                }
        }
    }

    @ViewBuilder
    private var actionButtons: some View {
        // Paying, updating a card and stopping auto-renew are the DRIVER's.
        // The server refuses them for anyone else, so offering them to an
        // owner would only ever produce a 403.
        if isOwner {
            EmptyView()
        } else {
            VStack(spacing: 8) {
                switch banner {
            case .arrears:
                // The recorded disclosure tells the driver they can settle
                // arrears "with Pay now". The button has to be that button.
                primaryButton("Pay now") { Task { await viewModel.payNow() } }
            case .authenticate:
                primaryButton("Approve payment") { Task { await viewModel.payNow() } }
            case .paymentFailed:
                primaryButton("Pay now") { Task { await viewModel.payNow() } }
                secondaryButton("Update card") { Task { await viewModel.startCardUpdate() } }
            case .cardProblem:
                primaryButton("Update card") { Task { await viewModel.startCardUpdate() } }
            case .needsCard:
                primaryButton("Add card") { Task { await viewModel.startCardUpdate() } }
            case .paused(let reason) where reason == "delinquent":
                // The halt's only exit is a card that works. Saying the
                // rental is ending without offering the fix would be a
                // dead end with money attached.
                primaryButton("Update card") { Task { await viewModel.startCardUpdate() } }
            case .collectionPending, .paused, .renewalStopped, .healthy:
                EmptyView()
            }

            // The everyday controls stay available in every healthy or
            // merely-paused state — a driver should not have to wait for
            // something to break to change their card or stop renewing.
            if canManage {
                HStack(spacing: 8) {
                    if banner != .cardProblem && banner != .needsCard && !isPaymentActionBanner {
                        secondaryButton("Update card") { Task { await viewModel.startCardUpdate() } }
                    }
                    if viewModel.status?.renewalsRunning == true {
                        secondaryButton("Stop auto-renew") { showStopRenewalConfirm = true }
                    }
                }
            }
            }
            .disabled(viewModel.isActing)
            .opacity(viewModel.isActing ? 0.6 : 1)
        }
    }

    // MARK: Stripe sheets

    @ViewBuilder
    private var paymentSheetHost: some View {
        if let intent = viewModel.pendingPayment,
           let customerId = intent.customerId, let ephemeralKey = intent.ephemeralKeySecret {
            PaymentSheetPresenter(
                clientSecret: intent.paymentIntentClientSecret,
                ephemeralKeySecret: ephemeralKey,
                customerId: customerId,
                publishableKey: intent.publishableKey
            ) { result in
                Task {
                    await viewModel.handlePaymentResult(result)
                    onChanged()
                }
            }
            .frame(width: 0, height: 0)
            .id(intent.paymentIntentId)
        }
    }

    @ViewBuilder
    private var cardUpdateSheetHost: some View {
        if let start = viewModel.pendingCardUpdate,
           let customerId = start.customerId, let ephemeralKey = start.ephemeralKeySecret {
            SetupSheetPresenter(
                setupIntentClientSecret: start.setupIntentClientSecret,
                ephemeralKeySecret: ephemeralKey,
                customerId: customerId,
                publishableKey: start.publishableKey
            ) { result in
                Task {
                    await viewModel.handleCardUpdateResult(result, setupIntentId: start.setupIntentId)
                    onChanged()
                }
            }
            .frame(width: 0, height: 0)
            .id(start.setupIntentId)
        }
    }

    // MARK: Pieces

    private enum Tone { case red, orange, secondaryTone }

    private func bannerBody(title: String, detail: String, tone: Tone) -> some View {
        let color: Color = {
            switch tone {
            case .red: return .red
            case .orange: return .orange
            case .secondaryTone: return .secondary
            }
        }()
        return VStack(alignment: .leading, spacing: 4) {
            Text(title)
                .font(.subheadline.weight(.semibold))
                .foregroundColor(color == .secondary ? .primary : color)
                .fixedSize(horizontal: false, vertical: true)
            Text(detail)
                .font(.caption)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func primaryButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 6) {
                if viewModel.isActing { ProgressView().tint(.white).scaleEffect(0.8) }
                Text(title).font(.system(size: 14, weight: .semibold))
            }
            .foregroundColor(.white)
            .frame(maxWidth: .infinity)
            .frame(height: TodayLayout.optionButtonHeight)
            .background(accentColor)
            .cornerRadius(8)
        }
    }

    private func secondaryButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title)
                .font(.system(size: 14, weight: .semibold))
                .foregroundColor(.primary)
                .frame(maxWidth: .infinity)
                .frame(height: TodayLayout.optionButtonHeight)
                .background(Color(.systemGray6))
                .cornerRadius(8)
        }
    }

    private func alertBinding(for message: Binding<String?>) -> Binding<Bool> {
        Binding(
            get: { message.wrappedValue != nil },
            set: { if !$0 { message.wrappedValue = nil } }
        )
    }

    // MARK: Derived

    private var needsAttention: Bool {
        switch banner {
        case .healthy, .renewalStopped: return false
        default: return true
        }
    }

    /// Post-return balance: the rental is over, so a schedule would be noise.
    private var isArrearsBanner: Bool {
        if case .arrears = banner { return true }
        return false
    }

    private var isPaymentActionBanner: Bool {
        switch banner {
        case .arrears, .authenticate, .paymentFailed: return true
        default: return false
        }
    }

    private var accentColor: Color {
        switch banner {
        case .arrears, .paymentFailed, .cardProblem: return .red
        case .authenticate, .paused, .collectionPending, .needsCard: return .orange
        case .renewalStopped, .healthy: return TodayLayout.tealAccent
        }
    }

    /// Card management only makes sense while a future charge exists.
    private var canManage: Bool {
        guard let status = viewModel.status else { return false }
        return !status.isReturned
    }

    private var endsLine: String? {
        guard let ends = viewModel.status?.rentalEndsAt else { return nil }
        if viewModel.status?.renewalsRunning == true {
            return "This week is paid through \(Self.day(ends))"
        }
        return "Rental ends \(Self.day(ends))"
    }

    // The only reasons the backend can write, fixed by the CHECK constraint
    // on renewal_halted_reason (migration 000054): delinquent, dispute,
    // consent_revoked, return_initiated. Anything else here would be copy
    // that never renders — which is how a driver ends up reading a generic
    // sentence while their rental is quietly terminating.
    private static func pausedTitle(_ reason: String) -> String {
        switch reason {
        case "delinquent": return "Weekly billing has stopped"
        case "dispute": return "Weekly billing is paused"
        case "return_initiated": return "Billing is paused while you return the car"
        default: return "Weekly billing is paused"
        }
    }

    private static func pausedDetail(_ reason: String) -> String {
        switch reason {
        case "delinquent":
            return "We couldn't collect a weekly payment and have stopped retrying, so no further weeks will be charged. Your rental ends when the time you've paid for runs out. Adding a working card restarts it."
        case "dispute":
            return "A payment on this rental is being reviewed by the card issuer. No new weeks will be charged while that's open."
        case "return_initiated":
            return "No weekly charges happen while a return is in progress. If the return is cancelled, billing resumes."
        default:
            return "No new weeks will be charged for now."
        }
    }

    private static func money(_ cents: Int64) -> String {
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = "USD"
        let value = Double(cents) / 100
        return formatter.string(from: NSNumber(value: value)) ?? String(format: "$%.2f", value)
    }

    private static func day(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "EEE, d MMM"
        return formatter.string(from: date)
    }
}
