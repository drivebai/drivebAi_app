import SwiftUI

/// My Cars tab view for Car Owners with segmented tabs (For rent / For sale)
struct OwnerMyCarsView: View {
    @StateObject private var store = OwnerCarsStore.shared
    @State private var selectedTab: CarListTab = .forRent
    @State private var showCreateListing = false

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
                    // Fetch cars from backend on view appear
                    await store.fetchCars()
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
            forRentCount: store.forRentCount,
            forSaleCount: store.forSaleCount
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
            ForEach(cars) { car in
                carNavigationLink(for: car)
            }
        }
        .padding(.horizontal, horizontalPadding)
        .padding(.vertical, spacing)
    }

    private func carNavigationLink(for car: Car) -> some View {
        NavigationLink(value: car) {
            CarListRow(car: car)
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
        Button(action: {}) {
            Image(systemName: "line.3.horizontal.decrease.circle")
                .foregroundColor(.primary)
        }
    }

    // MARK: - Filtered Cars

    private var filteredCars: [Car] {
        switch selectedTab {
        case .forRent:
            return store.carsForRent
        case .forSale:
            return store.carsForSale
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

    var body: some View {
        HStack(spacing: 12) {
            thumbnailView
            contentView
            Spacer()
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

        #if DEBUG
        let _ = print("[CarListRow] Rendering thumbnail for '\(car.title)': imageURL=\(coverSlot?.imageURL ?? "nil"), fullURL=\(coverSlot?.fullImageURL?.absoluteString ?? "nil"), hasLocalData=\(coverSlot?.localImageData != nil)")
        #endif

        if let imageURL = coverSlot?.fullImageURL {
            AsyncImage(url: imageURL) { phase in
                switch phase {
                case .success(let image):
                    image
                        .resizable()
                        .aspectRatio(contentMode: .fill)
                        .frame(width: 100, height: 80)
                        .clipShape(RoundedRectangle(cornerRadius: 12))
                case .failure(let error):
                    #if DEBUG
                    let _ = print("[CarListRow] AsyncImage FAILED for '\(car.title)': \(error)")
                    #endif
                    thumbnailPlaceholder
                case .empty:
                    thumbnailLoading
                @unknown default:
                    thumbnailPlaceholder
                }
            }
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

    private var thumbnailLoading: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 12)
                .fill(MyCarsLayout.tealAccent.opacity(0.08))
                .frame(width: 100, height: 80)

            ProgressView()
        }
    }

    private var contentView: some View {
        VStack(alignment: .leading, spacing: 6) {
            CarStatusChipSmall(status: car.status)
            titleText
            metadataRow
        }
    }

    private var titleText: some View {
        Text(car.title)
            .font(.subheadline)
            .fontWeight(.semibold)
            .foregroundColor(.primary)
            .lineLimit(1)
    }

    private var metadataRow: some View {
        HStack(spacing: 16) {
            if car.isForRent, let price = car.weeklyRentPrice {
                CarMetadataLabel(label: "Weekly", value: price.formatted)
            }
            CarMetadataLabel(label: "Rented", value: car.rentedWeeksFormatted)
            CarMetadataLabel(label: "Total", value: car.totalEarnedFormatted)
        }
    }

    private var chatButton: some View {
        Button(action: {}) {
            Image(systemName: "message")
                .font(.system(size: 16))
                .foregroundColor(MyCarsLayout.tealAccent)
                .frame(width: 40, height: 40)
                .background(chatButtonBackground)
        }
        .buttonStyle(PlainButtonStyle())
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

struct CarStatusChipSmall: View {
    let status: CarListingStatus

    init(status: CarListingStatus = .available) {
        self.status = status
    }

    private var statusIcon: String {
        switch status {
        case .available: return "checkmark.circle.fill"
        case .rented: return "key.fill"
        case .pending: return "clock.fill"
        case .paused: return "pause.circle.fill"
        }
    }

    private var statusColor: Color {
        switch status {
        case .available: return .green
        case .rented: return .blue
        case .pending: return .orange
        case .paused: return .gray
        }
    }

    private var statusText: String {
        switch status {
        case .available: return "Available"
        case .rented: return "Rented"
        case .pending: return "Pending"
        case .paused: return "Paused"
        }
    }

    var body: some View {
        HStack(spacing: 4) {
            Image(systemName: statusIcon)
                .font(.system(size: 8, weight: .semibold))
            Text(statusText)
                .font(.caption2)
                .fontWeight(.medium)
        }
        .foregroundColor(statusColor)
        .padding(.horizontal, 6)
        .padding(.vertical, 3)
        .background(statusColor.opacity(0.12))
        .cornerRadius(4)
    }
}

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
}
