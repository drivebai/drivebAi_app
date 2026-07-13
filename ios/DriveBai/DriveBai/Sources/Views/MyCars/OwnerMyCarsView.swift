import SwiftUI

/// My Cars tab view for Car Owners with segmented tabs (For rent / For sale)
struct OwnerMyCarsView: View {
    @StateObject private var store = OwnerCarsStore.shared
    @EnvironmentObject private var authStore: AuthStore
    @ObservedObject private var tour = ProductTourCoordinator.shared
    @State private var checklistCollapsed = false
    @State private var selectedTab: CarListTab = .forRent
    @State private var showCreateListing = false

    // QA pt 6a — status filter + sort, applied client-side on top of the
    // For rent / For sale tabs.
    @State private var showFilterSheet = false
    @State private var filter = MyCarsFilter()

    // QA pt 6b — chat shortcut on a rented car's card. Set when the owner
    // taps the message icon; drives an in-stack push to ChatView (same
    // pattern as OwnerTodayView's navigateToChatTask).
    @State private var chatTarget: MyCarsChatTarget?

    var body: some View {
        NavigationStack {
            mainContent
                .background(MyCarsLayout.pageBackground.ignoresSafeArea())
                .navigationTitle("Your cars")
                .navigationBarTitleDisplayMode(.large)
                .toolbar { toolbarContent }
                .navigationDestination(for: Car.self) { car in
                    CarDetailView(carId: car.id)
                }
                .navigationDestination(item: $chatTarget) { target in
                    if let userId = authStore.state.user?.id {
                        ChatView(
                            chatId: target.chatId,
                            currentUserId: userId,
                            counterpartyId: target.driverId,
                            counterpartyName: target.driverName,
                            initialTab: .messages
                        )
                    }
                }
                .sheet(isPresented: $showFilterSheet) {
                    MyCarsFilterSheet(filter: $filter)
                }
                .sheet(isPresented: $showCreateListing, onDismiss: {
                    // Re-fetch cars when the sheet is dismissed to ensure fresh data
                    #if DEBUG
                    print("[OwnerMyCarsView] Create listing sheet dismissed, refreshing cars...")
                    #endif
                    Task {
                        await store.fetchCars()
                    }
                }) {
                    CreateListingFlowView()
                }
                .task {
                    checklistCollapsed = tour.checklistUIState(role: .owner).collapsed
                    // Fetch cars from backend on view appear
                    await store.fetchCars()
                    if store.cars.isEmpty { tour.handle(.ownerCarCountZero) }
                }
                .onChange(of: store.cars.isEmpty) { _, isEmpty in
                    if isEmpty { tour.handle(.ownerCarCountZero) }
                }
                .onChange(of: checklistCollapsed) { _, collapsed in
                    tour.setChecklistCollapsed(collapsed, role: .owner)
                }
        }
    }

    // MARK: - Main Content

    private var mainContent: some View {
        VStack(spacing: 0) {
            segmentedTabs
            carListScrollView
        }
    }

    // MARK: - Segmented Tabs

    private var segmentedTabs: some View {
        MyCarsSegmentedTabs(
            selectedTab: $selectedTab,
            forRentCount: carsForTab(.forRent).count,
            forSaleCount: carsForTab(.forSale).count
        )
        .padding(.horizontal, MyCarsLayout.horizontalPadding)
        .padding(.top, 8)
    }

    // MARK: - Car List

    private var carListScrollView: some View {
        ScrollView {
            carListContent
        }
        .refreshable {
            await store.refresh()
        }
    }

    private var carListContent: some View {
        let cars = filteredCars
        let horizontalPadding = MyCarsLayout.horizontalPadding
        let spacing = MyCarsLayout.cardSpacing

        return LazyVStack(spacing: spacing) {
            if store.cars.isEmpty {
                // Fix for confusion #3: a zero-car owner used to see a blank
                // screen with only a "+" toolbar. Surface the Getting-started
                // checklist + a clear "List your first vehicle" call to action.
                zeroCarEmptyState
            } else {
                ForEach(cars) { car in
                    carNavigationLink(for: car)
                }
                if cars.isEmpty && filter.isActive {
                    filteredEmptyState
                }
            }
        }
        .padding(.horizontal, horizontalPadding)
        .padding(.vertical, spacing)
    }

    // MARK: - Zero-car empty state (fix for confusion #3)

    @ViewBuilder
    private var zeroCarEmptyState: some View {
        VStack(spacing: 20) {
            if !tour.checklistUIState(role: .owner).dismissed {
                ChecklistCard.owner(
                    hasCar: false,
                    docsComplete: false,
                    submittedForReview: false,
                    isLive: false,
                    collapsed: $checklistCollapsed,
                    onPrimaryAction: { showCreateListing = true },
                    onDismiss: { tour.dismissChecklist(role: .owner) }
                )
            } else {
                // Checklist dismissed — keep a plain, un-blank empty state so the
                // owner still has a way in besides the toolbar "+".
                VStack(spacing: 16) {
                    Image(systemName: "car.fill")
                        .font(.system(size: 52))
                        .foregroundColor(Color.driveBaiPrimary.opacity(0.4))
                    Text("No cars listed yet")
                        .font(.headline)
                    Text("List your first vehicle to start renting or selling it.")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                    Button("List your first vehicle") { showCreateListing = true }
                        .buttonStyle(.borderedProminent)
                        .tint(Color.driveBaiPrimary)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 40)
            }
        }
        .onboardingTarget(.myCarsEmpty)
    }

    private var filteredEmptyState: some View {
        VStack(spacing: 12) {
            Image(systemName: "line.3.horizontal.decrease.circle")
                .font(.system(size: 32))
                .foregroundColor(.secondary)
            Text("No cars match your filters")
                .font(.subheadline)
                .foregroundColor(.secondary)
            Button("Clear filters") {
                filter = MyCarsFilter()
            }
            .font(.subheadline.weight(.semibold))
            .foregroundColor(Color.driveBaiPrimary)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 40)
    }

    private func carNavigationLink(for car: Car) -> some View {
        NavigationLink(value: car) {
            CarListRow(car: car, onChatTap: {
                guard let rental = car.activeRental, let chatId = rental.chatId else { return }
                chatTarget = MyCarsChatTarget(
                    chatId: chatId,
                    driverId: rental.driverId,
                    driverName: rental.driverName
                )
            })
        }
        .buttonStyle(PlainButtonStyle())
    }

    // MARK: - Toolbar

    @ToolbarContentBuilder
    private var toolbarContent: some ToolbarContent {
        ToolbarItemGroup(placement: .navigationBarTrailing) {
            addButton
            filterButton
        }
    }

    private var addButton: some View {
        Button(action: { showCreateListing = true }) {
            Image(systemName: "plus")
                .foregroundColor(Color.driveBaiPrimary)
        }
    }

    private var filterButton: some View {
        Button(action: { showFilterSheet = true }) {
            Image(systemName: filter.isActive
                  ? "line.3.horizontal.decrease.circle.fill"
                  : "line.3.horizontal.decrease.circle")
                .foregroundColor(filter.isActive ? Color.driveBaiPrimary : .primary)
        }
        .accessibilityLabel(filter.isActive ? "Filter (active)" : "Filter")
    }

    // MARK: - Filtered Cars

    /// Tab membership. Deliberately does NOT exclude paused cars (unlike
    /// `OwnerCarsStore.carsForRent/carsForSale`): a paused listing must stay
    /// visible so the owner can find it, see its "Listing paused" pill and
    /// resume it from the edit screen (QA pts 3/9).
    private func carsForTab(_ tab: CarListTab) -> [Car] {
        switch tab {
        case .forRent:
            return store.cars.filter { $0.isForRent }
        case .forSale:
            return store.cars.filter { $0.isForSale }
        }
    }

    private var filteredCars: [Car] {
        filter.apply(to: carsForTab(selectedTab))
    }
}

// MARK: - Chat Navigation Target

/// Push target for the car-card chat shortcut (QA pt 6b). Carries just
/// enough of the active rental to construct ChatView.
private struct MyCarsChatTarget: Hashable {
    let chatId: UUID
    let driverId: UUID
    let driverName: String
}

// MARK: - Status Filter + Sort (QA pt 6a)

/// Client-side filter/sort state for the My Cars list. Status matching goes
/// through `CarBusinessState.forCar` — the one canonical derivation — so the
/// filter can never disagree with the pill on the card.
struct MyCarsFilter: Equatable {
    var statuses: Set<MyCarsStatusFilter> = []
    var sort: MyCarsSort = .newest

    var isActive: Bool { !statuses.isEmpty || sort != .newest }

    func apply(to cars: [Car]) -> [Car] {
        var result = cars
        if !statuses.isEmpty {
            result = result.filter { statuses.contains(MyCarsStatusFilter.forCar($0)) }
        }
        return sort.apply(to: result)
    }
}

enum MyCarsStatusFilter: String, CaseIterable, Identifiable {
    case available
    case rented
    case pending
    case paused
    case sold

    var id: String { rawValue }

    var label: String {
        switch self {
        case .available: return "Available"
        case .rented: return "Rented"
        case .pending: return "Awaiting approval"
        case .paused: return "Paused"
        case .sold: return "Sold"
        }
    }

    static func forCar(_ car: Car) -> MyCarsStatusFilter {
        switch CarBusinessState.forCar(car) {
        case .available: return .available
        case .rented: return .rented
        case .awaitingApproval, .pendingReview: return .pending
        case .paused: return .paused
        case .sold: return .sold
        }
    }
}

enum MyCarsSort: String, CaseIterable, Identifiable {
    case newest
    case priceHighFirst
    case priceLowFirst
    case earningsHighFirst

    var id: String { rawValue }

    var label: String {
        switch self {
        case .newest: return "Newest first"
        case .priceHighFirst: return "Price (high → low)"
        case .priceLowFirst: return "Price (low → high)"
        case .earningsHighFirst: return "Total earned"
        }
    }

    func apply(to cars: [Car]) -> [Car] {
        switch self {
        case .newest:
            return cars.sorted { $0.createdAt > $1.createdAt }
        case .priceHighFirst:
            return cars.sorted { price(of: $0) > price(of: $1) }
        case .priceLowFirst:
            return cars.sorted { price(of: $0) < price(of: $1) }
        case .earningsHighFirst:
            return cars.sorted { $0.totalEarned.amount > $1.totalEarned.amount }
        }
    }

    /// Sortable price: weekly rent when listed for rent, else sale price.
    private func price(of car: Car) -> Double {
        car.weeklyRentPrice?.amount ?? car.salePrice?.amount ?? 0
    }
}

// MARK: - Filter Sheet

struct MyCarsFilterSheet: View {
    @Binding var filter: MyCarsFilter
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            Form {
                Section("Status") {
                    ForEach(MyCarsStatusFilter.allCases) { status in
                        Button(action: { toggle(status) }) {
                            HStack {
                                Text(status.label)
                                    .foregroundColor(.primary)
                                Spacer()
                                if filter.statuses.contains(status) {
                                    Image(systemName: "checkmark")
                                        .font(.subheadline.weight(.semibold))
                                        .foregroundColor(Color.driveBaiPrimary)
                                }
                            }
                        }
                    }
                }

                Section("Sort by") {
                    Picker("Sort by", selection: $filter.sort) {
                        ForEach(MyCarsSort.allCases) { sort in
                            Text(sort.label).tag(sort)
                        }
                    }
                    .pickerStyle(.inline)
                    .labelsHidden()
                }
            }
            .navigationTitle("Filter & sort")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Clear") { filter = MyCarsFilter() }
                        .disabled(!filter.isActive)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    private func toggle(_ status: MyCarsStatusFilter) {
        if filter.statuses.contains(status) {
            filter.statuses.remove(status)
        } else {
            filter.statuses.insert(status)
        }
    }
}

// MARK: - Tab Enum

enum CarListTab: String, CaseIterable {
    case forRent = "for_rent"
    case forSale = "for_sale"

    func title(count: Int) -> String {
        switch self {
        case .forRent: return "For rent (\(count))"
        case .forSale: return "For sale (\(count))"
        }
    }
}

// MARK: - Layout Constants

enum MyCarsLayout {
    static let horizontalPadding: CGFloat = 16
    static let cardSpacing: CGFloat = 12
    static let cardCornerRadius: CGFloat = 16
    static let cardBorderWidth: CGFloat = 1
    static let cardBorderColor = Color(.systemGray4)
    static let pageBackground = Color(.systemBackground)
    static let tealAccent = Color.driveBaiPrimary
}

// MARK: - Segmented Tabs

struct MyCarsSegmentedTabs: View {
    @Binding var selectedTab: CarListTab
    let forRentCount: Int
    let forSaleCount: Int

    var body: some View {
        HStack(spacing: 0) {
            tabButton(for: .forRent, count: forRentCount)
            tabButton(for: .forSale, count: forSaleCount)
        }
    }

    private func tabButton(for tab: CarListTab, count: Int) -> some View {
        MyCarsTabButton(
            title: tab.title(count: count),
            isSelected: selectedTab == tab,
            action: { selectedTab = tab }
        )
    }
}

// MARK: - Tab Button

struct MyCarsTabButton: View {
    let title: String
    let isSelected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            tabContent
        }
        .buttonStyle(PlainButtonStyle())
    }

    private var tabContent: some View {
        VStack(spacing: 8) {
            tabLabel
            tabIndicator
        }
        .frame(maxWidth: .infinity)
    }

    private var tabLabel: some View {
        Text(title)
            .font(.subheadline)
            .fontWeight(isSelected ? .semibold : .regular)
            .foregroundColor(isSelected ? .primary : .secondary)
    }

    private var tabIndicator: some View {
        Rectangle()
            .fill(isSelected ? MyCarsLayout.tealAccent : Color.clear)
            .frame(height: 2)
    }
}

// MARK: - Car List Row

struct CarListRow: View {
    let car: Car
    /// Chat shortcut tap (QA pt 6b). The button only renders when the car
    /// has an active rental with a chat attached.
    var onChatTap: (() -> Void)? = nil

    var body: some View {
        HStack(spacing: 12) {
            thumbnailView
            contentView
            Spacer(minLength: 8)
            chatButton
        }
        .padding(12)
        .background(Color(.systemBackground))
        .cornerRadius(MyCarsLayout.cardCornerRadius)
        .overlay(borderOverlay)
    }

    @ViewBuilder
    private var thumbnailView: some View {
        let coverSlot = car.photoSlots.first { $0.slotType == .coverFront }

        if let imageURL = coverSlot?.fullImageURL {
            RemoteImage(url: imageURL, contentMode: .fill, maxPixelSize: 300)
                .frame(width: 100, height: 80)
                .clipShape(RoundedRectangle(cornerRadius: 12))
        } else if let localData = coverSlot?.localImageData,
                  let uiImage = UIImage(data: localData) {
            Image(uiImage: uiImage)
                .resizable()
                .aspectRatio(contentMode: .fill)
                .frame(width: 100, height: 80)
                .clipShape(RoundedRectangle(cornerRadius: 12))
        } else {
            thumbnailPlaceholder
        }
    }

    private var thumbnailPlaceholder: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 12)
                .fill(MyCarsLayout.tealAccent.opacity(0.08))
                .frame(width: 100, height: 80)

            Image(systemName: "car.fill")
                .font(.system(size: 28))
                .foregroundColor(MyCarsLayout.tealAccent.opacity(0.4))
        }
    }

    private var contentView: some View {
        VStack(alignment: .leading, spacing: 6) {
            // THE canonical status pill (QA pt 3). The pill itself never
            // truncates (fixedSize + layoutPriority inside CarStatusPill);
            // the title/metadata below carry lineLimit(1) so THEY yield on
            // narrow widths instead.
            CarStatusPill(state: .forCar(car), style: .compact)
            titleText
            metadataRow
            if let rental = car.activeRental {
                rentalSummaryLine(rental: rental)
            }
        }
    }

    private var titleText: some View {
        Text(car.title)
            .font(.subheadline)
            .fontWeight(.semibold)
            .foregroundColor(.primary)
            .lineLimit(1)
            .truncationMode(.tail)
    }

    private var metadataRow: some View {
        HStack(spacing: 16) {
            if car.isForRent, let price = car.weeklyRentPrice {
                CarMetadataLabel(label: "Weekly", value: price.formatted)
            }
            CarMetadataLabel(label: "Rented", value: car.rentedWeeksFormatted)
            CarMetadataLabel(label: "Total", value: car.totalEarnedFormatted)
        }
        .lineLimit(1)
    }

    /// Rental summary under the metadata row: renter, weeks, end date and
    /// earnings on a separate line that WRAPS on narrow widths (QA pt 3) —
    /// the pill above stays intact while this line takes the squeeze.
    private func rentalSummaryLine(rental: ActiveRentalSummary) -> some View {
        let formatter = DateFormatter()
        formatter.dateStyle = .medium
        formatter.timeStyle = .none
        let ends = formatter.string(from: rental.plannedEndAt)
        let earned = Money(amount: Double(rental.currentEarnedCents) / 100.0)

        let firstName = rental.driverName
            .split(separator: " ", maxSplits: 1)
            .first
            .map(String.init) ?? rental.driverName

        var parts: [String] = []
        if !firstName.isEmpty { parts.append(firstName) }
        parts.append("\(rental.weeks)w")
        parts.append("Ends ~\(ends)")
        parts.append("Earned \(earned.formatted)")

        return HStack(alignment: .firstTextBaseline, spacing: 6) {
            Image(systemName: "calendar")
                .font(.system(size: 10, weight: .medium))
                .foregroundColor(.secondary)
            Text(parts.joined(separator: " · "))
                .font(.caption2)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    /// Chat shortcut — rendered only when the active rental resolved a chat
    /// (`chat_id` from the wave-1 API). No rental or no chat → no button.
    @ViewBuilder
    private var chatButton: some View {
        if car.activeRental?.chatId != nil {
            Button(action: { onChatTap?() }) {
                Image(systemName: "message")
                    .font(.system(size: 16))
                    .foregroundColor(MyCarsLayout.tealAccent)
                    .frame(width: 40, height: 40)
                    .background(chatButtonBackground)
            }
            .buttonStyle(PlainButtonStyle())
            .accessibilityLabel("Open rental chat")
        }
    }

    private var chatButtonBackground: some View {
        RoundedRectangle(cornerRadius: 10)
            .stroke(MyCarsLayout.tealAccent, lineWidth: 1.5)
    }

    private var borderOverlay: some View {
        RoundedRectangle(cornerRadius: MyCarsLayout.cardCornerRadius)
            .stroke(MyCarsLayout.cardBorderColor, lineWidth: MyCarsLayout.cardBorderWidth)
    }
}

// MARK: - Supporting Views

struct CarMetadataLabel: View {
    let label: String
    let value: String

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(label)
                .font(.caption2)
                .foregroundColor(.secondary)
                .lineLimit(1)

            Text(value)
                .font(.caption)
                .fontWeight(.semibold)
                .foregroundColor(.primary)
                .lineLimit(1)
        }
    }
}

#Preview {
    OwnerMyCarsView()
        .environmentObject(AuthStore.shared)
}
