import SwiftUI

// MARK: - Chat Navigation Data

struct ChatNavigationData: Identifiable, Hashable {
    let chatId: UUID
    let currentUserId: UUID
    let counterpartyId: UUID
    let counterpartyName: String
    /// Tab to land on when the chat opens. Defaults to nil (Messages). The
    /// "View purchase" affordance passes `.requests` so the buyer lands on the
    /// existing purchase card.
    var initialTab: ChatTab? = nil

    var id: UUID { chatId }
}

struct DiscoverView: View {
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var likedStore: LikedListingsStore
    @StateObject private var viewModel = DiscoverViewModel.shared
    @State private var showMapView = false
    @State private var showSortOptions = false
    @State private var showFilterSheet = false

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                // Search bar row
                searchBarRow

                // Filter chips row
                filterChipsRow

                // Content
                if authStore.state.isAuthenticated {
                    AuthenticatedDiscoverContent(showMapView: $showMapView, showSortOptions: $showSortOptions)
                        .environmentObject(viewModel)
                        .environmentObject(likedStore)
                } else {
                    UnauthenticatedDiscoverContent()
                }
            }
            .navigationTitle("Discover")
            .navigationBarTitleDisplayMode(.large)
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    HStack(spacing: 12) {
                        // Map/List toggle
                        Button(action: { withAnimation(.easeInOut(duration: 0.25)) { showMapView.toggle() } }) {
                            HStack(spacing: 4) {
                                Image(systemName: showMapView ? "rectangle.grid.1x2" : "map")
                                    .font(.system(size: 12))
                                Text(showMapView ? "Card view" : "Map")
                                    .font(.caption)
                                    .fontWeight(.medium)
                            }
                            .foregroundColor(.primary)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 6)
                            .background(Color(.systemGray6))
                            .cornerRadius(16)
                        }

                        // Filter button — presents DiscoverFilterSheet.
                        // Badge shows the number of active advanced filters.
                        Button(action: { showFilterSheet = true }) {
                            ZStack(alignment: .topTrailing) {
                                Image(systemName: "slider.horizontal.3")
                                    .font(.system(size: 16))
                                    .foregroundColor(.primary)
                                    .padding(4)
                                if viewModel.filters.activeCount > 0 {
                                    Text("\(viewModel.filters.activeCount)")
                                        .font(.system(size: 10, weight: .bold))
                                        .foregroundColor(.white)
                                        .padding(.horizontal, 4)
                                        .padding(.vertical, 1)
                                        .frame(minWidth: 14, minHeight: 14)
                                        .background(Color.red)
                                        .clipShape(Capsule())
                                        .offset(x: 4, y: -4)
                                }
                            }
                        }
                    }
                }
            }
            .task {
                // Driver-only screen: announce the first Discover appearance so
                // the search/first-card coach-marks can run for a new user.
                ProductTourCoordinator.shared.handle(.discoverAppeared)
                await viewModel.fetchListings()
            }
            .sheet(isPresented: $showFilterSheet) {
                DiscoverFilterSheet(viewModel: viewModel)
            }
        }
    }

    // MARK: - Search Bar Row

    private var searchBarRow: some View {
        HStack(spacing: 12) {
            HStack(spacing: 8) {
                Image(systemName: "magnifyingglass")
                    .font(.system(size: 16))
                    .foregroundColor(.secondary)

                TextField("Search cars...", text: $viewModel.searchText)
                    .font(.subheadline)

                if !viewModel.searchText.isEmpty {
                    Button(action: { viewModel.searchText = "" }) {
                        Image(systemName: "xmark.circle.fill")
                            .font(.system(size: 16))
                            .foregroundColor(.secondary)
                    }
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .background(Color(.systemGray6))
            .cornerRadius(12)
        }
        .padding(.horizontal, 16)
        .padding(.top, 8)
        .onboardingTarget(.discoverSearch)
    }

    // MARK: - Filter Chips Row

    private var filterChipsRow: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(DiscoverFilter.allCases, id: \.self) { filter in
                    FilterChip(
                        title: filter.title,
                        icon: filter.icon,
                        isSelected: viewModel.selectedFilter == filter,
                        action: { viewModel.applyFilter(filter) }
                    )
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
        }
    }
}

// MARK: - Discover Filter

enum DiscoverFilter: CaseIterable {
    case all
    case available
    case nearby
    case popular
    case liked

    var title: String {
        switch self {
        case .all: return "All"
        case .available: return "Available now"
        case .nearby: return "Nearby"
        case .popular: return "Popular"
        case .liked: return "Liked"
        }
    }

    var icon: String? {
        switch self {
        case .all: return nil
        case .available: return "checkmark.circle.fill"
        case .nearby: return "location.fill"
        case .popular: return "star.fill"
        case .liked: return "heart.fill"
        }
    }
}

// MARK: - Filter Chip (Figma style)

struct FilterChip: View {
    let title: String
    var icon: String?
    let isSelected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                if let icon = icon {
                    Image(systemName: icon)
                        .font(.system(size: 12))
                }
                Text(title)
                    .font(.subheadline)
                    .fontWeight(isSelected ? .semibold : .regular)
            }
            .foregroundColor(isSelected ? .white : .primary)
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .background(
                RoundedRectangle(cornerRadius: 20)
                    .fill(isSelected ? Color.driveBaiPrimary : Color(.systemGray6))
            )
            .overlay(
                RoundedRectangle(cornerRadius: 20)
                    .stroke(isSelected ? Color.clear : Color(.systemGray4), lineWidth: 1)
            )
        }
    }
}

// MARK: - Authenticated Content

struct AuthenticatedDiscoverContent: View {
    @EnvironmentObject private var viewModel: DiscoverViewModel
    @EnvironmentObject private var likedStore: LikedListingsStore
    @ObservedObject private var tour = ProductTourCoordinator.shared
    @Binding var showMapView: Bool
    @Binding var showSortOptions: Bool
    @State private var selectedMapCar: Car?
    @State private var checklistCollapsed = false

    /// Listings filtered by liked state when "Liked" filter is selected
    private var displayedListings: [Car] {
        if viewModel.selectedFilter == .liked {
            return viewModel.filteredListings.filter { likedStore.isLiked($0.id) }
        }
        return viewModel.filteredListings
    }

    private var displayedCount: Int {
        displayedListings.count
    }

    var body: some View {
        ZStack(alignment: .top) {
            if viewModel.isLoading && viewModel.listings.isEmpty {
                loadingView
            } else if showMapView {
                mapView
            } else if displayedListings.isEmpty && viewModel.error == nil {
                emptyStateView
            } else {
                listingsScrollView
            }

            // Error banner (non-blocking)
            if let error = viewModel.error, !viewModel.listings.isEmpty {
                errorBanner(message: error)
            }
        }
        .task {
            // Restore the checklist's collapse state, as the Today views do —
            // otherwise collapsing it here is forgotten the moment you leave.
            checklistCollapsed = tour.checklistUIState(role: .driver).collapsed
            // Fetch liked listings from backend if not already loaded
            if likedStore.likedIDs.isEmpty && !likedStore.isLoading {
                await likedStore.fetchLikedListings()
            }
        }
        .onChange(of: checklistCollapsed) { _, collapsed in
            tour.setChecklistCollapsed(collapsed, role: .driver)
        }
    }

    private var loadingView: some View {
        VStack(spacing: 16) {
            ProgressView()
                .scaleEffect(1.2)
            Text("Loading listings...")
                .font(.subheadline)
                .foregroundColor(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var mapView: some View {
        DiscoverMapView(
            listings: displayedListings,
            selectedCar: $selectedMapCar
        )
        .environmentObject(likedStore)
        .navigationDestination(for: Car.self) { car in
            ListingDetailView(car: car)
                .environmentObject(likedStore)
        }
    }

    private var emptyStateView: some View {
        VStack(spacing: 16) {
            // Getting-started banner for a new driver landing on an empty
            // Discover (Section 8). Hidden once dismissed or fully complete.
            if viewModel.selectedFilter != .liked,
               !tour.checklistUIState(role: .driver).dismissed {
                // Rows reflect what the user has actually done — never which
                // coach marks they happened to see.
                ChecklistCard.driver(
                    hasLicense: AuthStore.shared.hasRequiredDocuments(),
                    browsedCars: tour.hasMilestone(.viewedCarDetail),
                    sentRequest: tour.hasMilestone(.sentLeaseRequest),
                    foundWhereToPay: tour.hasMilestone(.openedRequestsTab),
                    collapsed: $checklistCollapsed,
                    onDismiss: { tour.dismissChecklist(role: .driver) }
                )
                .padding(.horizontal, 16)
                .padding(.bottom, 8)
            }

            Image(systemName: viewModel.selectedFilter == .liked ? "heart.slash" : "car.2")
                .font(.system(size: 50))
                .foregroundColor(.secondary)

            Text(viewModel.selectedFilter == .liked ? "No liked listings yet" : "No listings found")
                .font(.headline)

            if viewModel.selectedFilter == .liked {
                Text("Tap the heart on listings to save them here")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            } else if !viewModel.searchText.isEmpty {
                Text("Try adjusting your search")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            } else if let error = viewModel.error {
                Text(error)
                    .font(.subheadline)
                    .foregroundColor(.red)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal)
            } else {
                Text("Check back later for new cars")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func errorBanner(message: String) -> some View {
        HStack(spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundColor(.orange)
            Text("Couldn't refresh. Showing cached results.")
                .font(.caption)
                .foregroundColor(.secondary)
            Spacer()
            Button("Retry") {
                Task { await viewModel.refresh() }
            }
            .font(.caption)
            .fontWeight(.semibold)
            .foregroundColor(.driveBaiPrimary)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(Color(.systemBackground))
        .cornerRadius(8)
        .shadow(color: .black.opacity(0.1), radius: 4, y: 2)
        .padding(.horizontal, 16)
        .padding(.top, 8)
        .transition(.move(edge: .top).combined(with: .opacity))
        .animation(.easeInOut, value: viewModel.error != nil)
    }

    private var listingsScrollView: some View {
        ScrollView {
            VStack(spacing: 0) {
                // Results header row
                resultsHeaderRow
                    .padding(.horizontal, 16)
                    .padding(.top, 8)
                    .padding(.bottom, 16)

                // Listings grid
                LazyVStack(spacing: 16) {
                    ForEach(displayedListings) { car in
                        NavigationLink(value: car) {
                            DiscoverListingCard(car: car)
                                .environmentObject(likedStore)
                        }
                        .buttonStyle(PlainButtonStyle())
                        // Coach-mark anchor for `driver_first_discover_v1` —
                        // only the first result card is spotlighted.
                        .onboardingTargetIf(car.id == displayedListings.first?.id, .discoverFirstCard)
                    }
                }
                .padding(.horizontal, 16)
                .padding(.bottom, 32)
            }
        }
        .refreshable {
            await viewModel.refresh()
        }
        .navigationDestination(for: Car.self) { car in
            ListingDetailView(car: car)
                .environmentObject(likedStore)
        }
    }

    private var resultsHeaderRow: some View {
        HStack {
            Text("\(displayedCount) results")
                .font(.subheadline)
                .foregroundColor(.secondary)

            Spacer()

            Button(action: { showSortOptions = true }) {
                HStack(spacing: 4) {
                    Text("Sort")
                        .font(.subheadline)
                        .fontWeight(.medium)
                    Image(systemName: "chevron.down")
                        .font(.system(size: 12))
                }
                .foregroundColor(.primary)
            }
        }
    }
}

// MARK: - Unauthenticated Content

struct UnauthenticatedDiscoverContent: View {
    var body: some View {
        VStack(spacing: 32) {
            Spacer()

            VStack(spacing: 16) {
                Image(systemName: "car.2.fill")
                    .font(.system(size: 60))
                    .foregroundColor(.driveBaiPrimary)

                Text("Find your perfect ride")
                    .font(.title2)
                    .fontWeight(.bold)

                Text("Sign in to browse available cars and connect with owners")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 32)
            }

            Spacer()
        }
    }
}

// MARK: - Discover Listing Card (Figma Design)

struct DiscoverListingCard: View {
    let car: Car
    @EnvironmentObject private var likedStore: LikedListingsStore
    @State private var showShareSheet = false

    private var isLiked: Bool {
        likedStore.isLiked(car.id)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Image with overlays
            ZStack(alignment: .topLeading) {
                carImageView
                    .frame(height: 200)
                    .clipped()

                // Top row: Badge and action icons
                HStack {
                    // Availability badge
                    if car.status == .available {
                        AvailabilityBadge(text: "Available now!")
                    }

                    Spacer()

                    // Action icons - prevent NavigationLink tap propagation
                    HStack(spacing: 8) {
                        CircleIconButton(icon: "square.and.arrow.up") {
                            showShareSheet = true
                        }
                        CircleIconButton(
                            icon: isLiked ? "heart.fill" : "heart",
                            tintColor: isLiked ? .red : .white
                        ) {
                            withAnimation(.spring(response: 0.3, dampingFraction: 0.6)) {
                                likedStore.toggleLike(car.id)
                            }
                        }
                    }
                }
                .padding(12)
            }
            .clipShape(RoundedRectangle(cornerRadius: 16))
            .sheet(isPresented: $showShareSheet) {
                ShareSheet(items: [shareText])
            }

            // Card content
            VStack(alignment: .leading, spacing: 8) {
                // Title
                Text(car.displayTitle)
                    .font(.headline)
                    .foregroundColor(.primary)
                    .lineLimit(1)

                // Price and distance row
                HStack {
                    if let price = car.weeklyRentPrice {
                        Text("\(price.formatted)")
                            .font(.subheadline)
                            .fontWeight(.semibold)
                            .foregroundColor(.driveBaiPrimary)
                        +
                        Text(" per week")
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                    }

                    Spacer()

                    // Distance
                    HStack(spacing: 4) {
                        Image(systemName: "location.fill")
                            .font(.system(size: 12))
                        Text(car.location.distanceMiles > 0 ? String(format: "%.1f mi", car.location.distanceMiles) : car.location.neighborhood)
                            .font(.caption)
                    }
                    .foregroundColor(.secondary)
                }

                // Requirements row
                // Deposit pill removed (QA pt 7): deposits are gone from
                // the product; the backend now always serves 0.
                HStack(spacing: 16) {
                    RequirementPill(
                        icon: "calendar",
                        text: "\(car.requirements.minYearsLicensedDriving)+ years licensed"
                    )
                }
            }
            .padding(.horizontal, 4)
            .padding(.top, 12)
            .padding(.bottom, 4)
        }
        .padding(12)
        .background(Color(.systemBackground))
        .cornerRadius(20)
        .shadow(color: .black.opacity(0.06), radius: 12, x: 0, y: 4)
    }

    @ViewBuilder
    private var carImageView: some View {
        let coverSlot = car.photoSlots.first { $0.slotType == .coverFront }

        if let imageURL = coverSlot?.fullImageURL {
            RemoteImage(url: imageURL, contentMode: .fill)
        } else if let localData = coverSlot?.localImageData,
                  let uiImage = UIImage(data: localData) {
            Image(uiImage: uiImage)
                .resizable()
                .aspectRatio(contentMode: .fill)
        } else {
            imagePlaceholder
        }
    }

    private var imagePlaceholder: some View {
        Rectangle()
            .fill(Color(.systemGray5))
            .overlay(
                Image(systemName: "car.fill")
                    .font(.system(size: 40))
                    .foregroundColor(.gray)
            )
    }

    private var imageLoadingPlaceholder: some View {
        Rectangle()
            .fill(Color(.systemGray5))
            .overlay(ProgressView())
    }

    private var shareText: String {
        "\(car.displayTitle) - Check out this car on DriveBai! drivebai://listing/\(car.id)"
    }
}

// MARK: - Share Sheet (UIActivityViewController wrapper)

struct ShareSheet: UIViewControllerRepresentable {
    let items: [Any]

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: items, applicationActivities: nil)
    }

    func updateUIViewController(_ uiViewController: UIActivityViewController, context: Context) {}
}

// MARK: - Supporting Views

struct AvailabilityBadge: View {
    let text: String

    var body: some View {
        HStack(spacing: 4) {
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 10))
            Text(text)
                .font(.caption)
                .fontWeight(.semibold)
        }
        .foregroundColor(.white)
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(Color.green)
        .cornerRadius(8)
    }
}

struct CircleIconButton: View {
    let icon: String
    var tintColor: Color = .white
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: icon)
                .font(.system(size: 14))
                .foregroundColor(tintColor)
                .frame(width: 32, height: 32)
                .background(Color.black.opacity(0.4))
                .clipShape(Circle())
        }
        .contentShape(Circle()) // Ensure proper hit testing
    }
}

struct RequirementPill: View {
    let icon: String
    let text: String

    var body: some View {
        HStack(spacing: 4) {
            Image(systemName: icon)
                .font(.system(size: 11))
            Text(text)
                .font(.caption)
        }
        .foregroundColor(.secondary)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(Color(.systemGray6))
        .cornerRadius(6)
    }
}

// MARK: - Listing Detail View (Figma Design)

struct ListingDetailView: View {
    let car: Car
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var likedStore: LikedListingsStore
    @EnvironmentObject private var authStore: AuthStore
    @State private var showShareSheet = false
    @State private var currentPhotoIndex = 0
    @State private var showLocationMap = false
    @State private var navigateToChat: ChatNavigationData?
    @State private var isRequestingLease = false
    @State private var leaseRequestError: String?
    /// Present the "Buy this car" offer sheet.  Non-nil while it's on
    /// screen so we can hand the Car through by identity binding.
    @State private var buyRequestCar: Car?
    /// The current user's existing non-terminal purchase for THIS car, if any.
    /// When set, the "Buy this car" CTA is replaced with a status pill + a
    /// "View purchase" action so the buyer can't dead-end into a backend 409
    /// ("Offer failed"). Cross-referenced against the buyer's Today purchase
    /// list; the 409 stays the authoritative backstop.
    @State private var activePurchase: PurchaseRequest?

    private var isLiked: Bool {
        likedStore.isLiked(car.id)
    }

    /// Photo slots that have images (either local data or URL)
    private var photosWithImages: [CarPhotoSlot] {
        car.photoSlots.filter { $0.hasImage }.sorted { $0.slotType.sortOrder < $1.slotType.sortOrder }
    }

    var body: some View {
        GeometryReader { outerGeometry in
            let safeAreaTop = outerGeometry.safeAreaInsets.top

            ZStack(alignment: .top) {
                // Main scrollable content
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        // Hero image gallery - extends into safe area
                        ZStack(alignment: .bottom) {
                            // Neutral background for fitted-image letterboxing
                            CarDetailUI.heroBackground

                            if photosWithImages.isEmpty {
                                imagePlaceholder(height: CarDetailUI.heroHeight + safeAreaTop)
                            } else {
                                TabView(selection: $currentPhotoIndex) {
                                    ForEach(Array(photosWithImages.enumerated()), id: \.element.id) { index, slot in
                                        photoView(for: slot, height: CarDetailUI.heroHeight + safeAreaTop)
                                            .tag(index)
                                    }
                                }
                                .tabViewStyle(.page(indexDisplayMode: .never))
                            }

                            // Subtle gradient for polished safe-area transition
                            VStack {
                                LinearGradient(
                                    colors: [.black.opacity(0.25), .clear],
                                    startPoint: .top,
                                    endPoint: .bottom
                                )
                                .frame(height: safeAreaTop + 56)
                                Spacer()
                            }
                            .allowsHitTesting(false)

                            // Bottom overlay: badge and page indicator
                            HStack {
                                if car.status == .available {
                                    AvailabilityBadge(text: "Available now!")
                                }
                                Spacer()
                                if photosWithImages.count > 1 {
                                    pageIndicator
                                }
                            }
                            .padding(16)
                        }
                        .frame(height: CarDetailUI.heroHeight + safeAreaTop)

                        // Content
                        VStack(alignment: .leading, spacing: 20) {
                            // Title row with location icon button
                            HStack(alignment: .top) {
                                VStack(alignment: .leading, spacing: 8) {
                                    Text(car.displayTitle)
                                        .font(.title2)
                                        .fontWeight(.bold)

                                    HStack(spacing: 4) {
                                        Image(systemName: "mappin")
                                            .font(.system(size: 14))
                                        Text(car.location.neighborhood)
                                        if car.location.distanceMiles > 0 {
                                            Text("• \(String(format: "%.1f mi away", car.location.distanceMiles))")
                                        }
                                    }
                                    .font(.subheadline)
                                    .foregroundColor(.secondary)
                                }

                                Spacer()

                                // Location icon button
                                if car.location.hasCoordinate {
                                    Button(action: { showLocationMap = true }) {
                                        Image(systemName: "location.fill")
                                            .font(.system(size: 14))
                                            .foregroundColor(.driveBaiPrimary)
                                            .frame(width: 36, height: 36)
                                            .background(Color.driveBaiPrimary.opacity(0.1))
                                            .clipShape(Circle())
                                    }
                                }
                            }

                            // Pricing cards
                            HStack(spacing: 12) {
                                if car.isForRent, let price = car.weeklyRentPrice {
                                    PricingCard(
                                        title: "Rent",
                                        price: price.formatted,
                                        subtitle: "per week",
                                        isPrimary: true
                                    )
                                }
                                if car.isForSale, let price = car.salePrice {
                                    PricingCard(
                                        title: "Buy",
                                        price: price.formatted,
                                        subtitle: "purchase",
                                        isPrimary: !car.isForRent
                                    )
                                }
                            }

                            Divider()

                            // Description
                            if !car.description.isEmpty {
                                VStack(alignment: .leading, spacing: 8) {
                                    Text("About this car")
                                        .font(.headline)
                                    Text(car.description)
                                        .font(.subheadline)
                                        .foregroundColor(.secondary)
                                }

                                Divider()
                            }

                            // Specifications
                            specsSection

                            Divider()

                            // Requirements - 2 horizontal items with big value + caption
                            requirementsSection

                            // Bottom spacing for sticky bar
                            Spacer()
                                .frame(height: 100)
                        }
                        .padding(16)
                    }
                }
                .scrollIndicators(.hidden)
                .ignoresSafeArea(edges: .top)

                // Top overlay controls - positioned below safe area, on top of image
                topOverlayControls
            }
            .safeAreaInset(edge: .bottom) {
                bottomCTABar
            }
        }
        .navigationBarHidden(true)
        .onAppear {
            // Feed the for-sale flag into the tour context so the "Or buy it"
            // coach-mark only enqueues for cars that are actually for sale,
            // then announce the detail open.
            ProductTourCoordinator.shared.updateContext { $0.carIsForSale = car.isForSale }
            ProductTourCoordinator.shared.handle(.carDetailOpened)
            // Opening a listing is the real "browsed cars" event, independent of
            // whether the coach mark was eligible to run.
            ProductTourCoordinator.shared.recordMilestone(.viewedCarDetail)
        }
        .task {
            // Suppress the Buy CTA when the current user already has an active
            // purchase for this car. Re-runs on every appearance, so returning
            // from the offer sheet / chat re-reconciles the button state.
            await loadActivePurchase()
        }
        .sheet(isPresented: $showShareSheet) {
            ShareSheet(items: [shareText])
        }
        .fullScreenCover(isPresented: $showLocationMap) {
            CarLocationMapView(car: car)
        }
        .navigationDestination(item: $navigateToChat) { data in
            ChatView(
                chatId: data.chatId,
                currentUserId: data.currentUserId,
                counterpartyId: data.counterpartyId,
                counterpartyName: data.counterpartyName,
                initialTab: data.initialTab
            )
        }
        .sheet(item: $buyRequestCar) { car in
            BuyRequestSheet(car: car) { chatId in
                guard let user = authStore.state.user else { return }
                navigateToChat = ChatNavigationData(
                    chatId: chatId,
                    currentUserId: user.id,
                    counterpartyId: car.owner.id,
                    counterpartyName: car.owner.name
                )
                Task { await ChatsListViewModel.shared.fetchChats() }
            }
            .environmentObject(authStore)
        }
        .alert("Lease Request Failed", isPresented: .constant(leaseRequestError != nil)) {
            Button("OK") { leaseRequestError = nil }
        } message: {
            if let error = leaseRequestError {
                Text(error)
            }
        }
    }

    /// Cross-reference the buyer's Today purchase list to find an in-flight
    /// purchase for this exact car. Reuses the same `/today/purchase-requests`
    /// endpoint the Today tab already consumes. Silent on failure — the
    /// backend 409 remains the authoritative guard.
    private func loadActivePurchase() async {
        guard car.isForSale, let userId = authStore.state.user?.id else { return }
        do {
            let response = try await APIClient.shared.fetchPurchaseRequestsToday()
            let purchases = response.purchaseRequests.map { $0.toDomain() }
            activePurchase = purchases.first { pr in
                pr.carId == car.id && pr.buyerId == userId && !pr.status.isTerminal
            }
        } catch {
            #if DEBUG
            print("[ListingDetailView] loadActivePurchase error: \(error)")
            #endif
        }
    }

    private func requestLease() {
        guard authStore.state.user != nil, !isRequestingLease else { return }
        // The "What happens next?" card's button reads "Send request", so it must
        // actually gate the POST. When the explainer isn't due, this sends
        // straight away.
        ProductTourCoordinator.shared.startOrRun(.driverPreRequest) {
            sendLeaseRequest()
        }
    }

    private func sendLeaseRequest() {
        guard let user = authStore.state.user, !isRequestingLease else { return }
        isRequestingLease = true

        Task {
            defer { isRequestingLease = false }
            do {
                let request = CreateLeaseRequestAPIRequest(weeks: 1, message: nil)
                let response = try await APIClient.shared.createLeaseRequest(listingId: car.id, request: request)

                // A real domain milestone: the request exists on the server.
                ProductTourCoordinator.shared.recordMilestone(.sentLeaseRequest)

                // Navigate to the chat and show the Requests tab
                navigateToChat = ChatNavigationData(
                    chatId: response.chatId,
                    currentUserId: user.id,
                    counterpartyId: car.owner.id,
                    counterpartyName: car.owner.name
                )

                // Teach where the driver pays *after* pushing the chat: the
                // Requests segment this coach-mark points at only exists once
                // that screen is on stack.
                ProductTourCoordinator.shared.handle(.leaseRequestCreated)

                // Refresh chats list
                await ChatsListViewModel.shared.fetchChats()
            } catch let apiError as APIError {
                leaseRequestError = apiError.errorDescription
            } catch {
                leaseRequestError = error.localizedDescription
            }
        }
    }

    // MARK: - Top Overlay Controls (Safe Area Aware)

    private var topOverlayControls: some View {
        HStack {
            // Back button - 44x44 tappable area for accessibility
            Button(action: { dismiss() }) {
                Image(systemName: "chevron.left")
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundColor(.white)
                    .frame(width: 44, height: 44)
                    .background(Color.black.opacity(0.4))
                    .clipShape(Circle())
            }
            .buttonStyle(.plain)
            .contentShape(Circle())

            Spacer()

            HStack(spacing: 12) {
                // Share button
                Button(action: { showShareSheet = true }) {
                    Image(systemName: "square.and.arrow.up")
                        .font(.system(size: 14, weight: .medium))
                        .foregroundColor(.white)
                        .frame(width: 44, height: 44)
                        .background(Color.black.opacity(0.4))
                        .clipShape(Circle())
                }
                .buttonStyle(.plain)
                .contentShape(Circle())

                // Favorite button
                Button(action: {
                    withAnimation(.spring(response: 0.3, dampingFraction: 0.6)) {
                        likedStore.toggleLike(car.id)
                    }
                }) {
                    Image(systemName: isLiked ? "heart.fill" : "heart")
                        .font(.system(size: 14, weight: .medium))
                        .foregroundColor(isLiked ? .red : .white)
                        .frame(width: 44, height: 44)
                        .background(Color.black.opacity(0.4))
                        .clipShape(Circle())
                }
                .buttonStyle(.plain)
                .contentShape(Circle())
            }
        }
        .padding(.horizontal, 16)
        .padding(.top, 8) // Small padding - container already respects safe area
    }

    // MARK: - Share Text

    private var shareText: String {
        "\(car.displayTitle) - Check out this car on DriveBai! drivebai://listing/\(car.id)"
    }

    // MARK: - Image Gallery Helpers

    @ViewBuilder
    private func photoView(for slot: CarPhotoSlot, height: CGFloat) -> some View {
        if let data = slot.localImageData, let uiImage = UIImage(data: data) {
            Image(uiImage: uiImage)
                .resizable()
                .scaledToFit()
                .frame(height: height)
        } else if let fullURL = slot.fullImageURL {
            RemoteImage(url: fullURL, contentMode: .fit)
                .frame(height: height)
        } else {
            imagePlaceholder(height: height)
        }
    }

    private var pageIndicator: some View {
        HStack(spacing: 6) {
            ForEach(0..<photosWithImages.count, id: \.self) { index in
                Circle()
                    .fill(index == currentPhotoIndex ? Color.white : Color.white.opacity(0.5))
                    .frame(width: 8, height: 8)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(Color.black.opacity(0.3))
        .cornerRadius(12)
    }

    private func imagePlaceholder(height: CGFloat) -> some View {
        Rectangle()
            .fill(Color(.systemGray5))
            .frame(height: height)
            .overlay(
                Image(systemName: "car.fill")
                    .font(.system(size: 50))
                    .foregroundColor(.gray)
            )
    }

    // MARK: - Specs Section

    private var specsSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Specifications")
                .font(.headline)

            LazyVGrid(columns: [
                GridItem(.flexible()),
                GridItem(.flexible())
            ], spacing: 12) {
                SpecCard(icon: "calendar", label: "Year", value: "\(car.specs.year)")
                SpecCard(icon: "car.fill", label: "Body", value: car.specs.bodyType.rawValue)
                SpecCard(icon: "fuelpump.fill", label: "Fuel", value: car.specs.fuelType.rawValue)
                SpecCard(icon: "speedometer", label: "Mileage", value: car.specs.mileageFormatted)
            }
        }
    }

    // MARK: - Requirements Section (2 horizontal items, big value + caption)

    private var requirementsSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Requirements")
                .font(.headline)

            HStack(spacing: 12) {
                // License requirement
                RequirementCard(
                    icon: "person.text.rectangle",
                    value: "\(car.requirements.minYearsLicensedDriving)+",
                    caption: "years licensed"
                )
                // Deposit card removed (QA pt 7) — deposits no longer exist
                // in the product.
            }
        }
    }

    // MARK: - Bottom CTA Bar
    //
    // Single primary "Request lease" button. The standalone "Message" CTA
    // was removed deliberately: drivers should reach the owner only after
    // a lease request exists. The owner-driver chat opens automatically
    // when `requestLease()` succeeds (via the chatId returned in the
    // CreateLeaseRequest response — see line just below), so no chat
    // capability is lost; it's just gated behind the request.

    private var bottomCTABar: some View {
        VStack(spacing: 0) {
            Divider()

            // Rent + Buy CTAs stack vertically when the listing is both.
            // Both remain optional so cars set for-rent-only or for-sale-
            // only render exactly one button.
            VStack(spacing: 10) {
                if car.isForRent {
                    Button(action: requestLease) {
                        HStack(spacing: 6) {
                            if isRequestingLease {
                                ProgressView()
                                    .tint(.white)
                            }
                            Text("Request lease")
                                .font(.headline)
                        }
                        .foregroundColor(.white)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 14)
                        .background(Color.driveBaiPrimary)
                        .cornerRadius(12)
                    }
                    .disabled(isRequestingLease)
                    .onboardingTarget(.requestLeaseCTA)
                }

                if car.isForSale {
                    if let purchase = activePurchase {
                        purchaseInProgressCTA(purchase)
                    } else {
                        buyThisCarButton
                    }
                }
            }
            .padding(16)
            .background(Color(.systemBackground))
        }
    }

    /// The default green "Buy this car" CTA (no active purchase yet).
    private var buyThisCarButton: some View {
        Button {
            // Explain buying *before* opening the offer form: the
            // intro's first card spotlights this very button, which
            // only exists on this screen. The sheet then presents
            // when the intro finishes (or immediately, if it's
            // already been seen).
            ProductTourCoordinator.shared.updateContext { $0.carIsForSale = true }
            ProductTourCoordinator.shared.startOrRun(.purchaseIntro) {
                buyRequestCar = car
            }
        } label: {
            HStack(spacing: 8) {
                Image(systemName: "cart.fill")
                VStack(alignment: .leading, spacing: 2) {
                    Text("Buy this car")
                        .font(.headline)
                    if let price = car.salePrice {
                        Text("Sale price \(price.formatted)")
                            .font(.caption)
                            .foregroundColor(.white.opacity(0.9))
                    }
                }
            }
            .foregroundColor(.white)
            .frame(maxWidth: .infinity)
            .padding(.vertical, 14)
            .background(Color.green)
            .cornerRadius(12)
        }
        .onboardingTarget(.buyThisCarCTA)
    }

    /// Replacement CTA shown when the buyer already has a non-terminal
    /// purchase for this car: a status pill + a "View purchase" action that
    /// routes to the existing purchase card in Chat → Requests.
    private func purchaseInProgressCTA(_ purchase: PurchaseRequest) -> some View {
        let waitingOnSeller = purchase.status == .requested
        return VStack(spacing: 10) {
            HStack(spacing: 6) {
                Image(systemName: waitingOnSeller ? "hourglass" : "checkmark.seal.fill")
                Text(waitingOnSeller ? "Waiting for seller" : "Purchase in progress")
            }
            .font(.subheadline.weight(.semibold))
            .foregroundColor(.driveBaiPrimary)
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .background(Color.driveBaiPrimary.opacity(0.12))
            .cornerRadius(12)

            Button {
                guard let user = authStore.state.user else { return }
                navigateToChat = ChatNavigationData(
                    chatId: purchase.chatId,
                    currentUserId: user.id,
                    counterpartyId: purchase.sellerId,
                    counterpartyName: purchase.sellerName,
                    initialTab: .requests
                )
            } label: {
                Text("View purchase")
                    .font(.headline)
                    .foregroundColor(.white)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 14)
                    .background(Color.driveBaiPrimary)
                    .cornerRadius(12)
            }
        }
    }
}

// MARK: - Requirement Card (Figma style - big value + caption)

struct RequirementCard: View {
    let icon: String
    let value: String
    let caption: String

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .font(.system(size: 20))
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 40, height: 40)
                .background(Color.driveBaiPrimary.opacity(0.1))
                .cornerRadius(10)

            VStack(alignment: .leading, spacing: 2) {
                Text(value)
                    .font(.title3)
                    .fontWeight(.bold)
                    .foregroundColor(.primary)
                Text(caption)
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            Spacer()
        }
        .padding(12)
        .frame(maxWidth: .infinity)
        .background(Color(.systemGray6))
        .cornerRadius(12)
    }
}

// MARK: - Detail View Supporting Components

struct PricingCard: View {
    let title: String
    let price: String
    let subtitle: String
    var isPrimary: Bool = false

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title)
                .font(.caption)
                .fontWeight(.medium)
                .foregroundColor(.secondary)

            Text(price)
                .font(.title3)
                .fontWeight(.bold)
                .foregroundColor(isPrimary ? .driveBaiPrimary : .primary)

            Text(subtitle)
                .font(.caption)
                .foregroundColor(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(isPrimary ? Color.driveBaiPrimary.opacity(0.08) : Color(.systemGray6))
        .cornerRadius(12)
    }
}

struct SpecCard: View {
    let icon: String
    let label: String
    let value: String

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: icon)
                .font(.system(size: 16))
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 32, height: 32)
                .background(Color.driveBaiPrimary.opacity(0.1))
                .cornerRadius(8)

            VStack(alignment: .leading, spacing: 2) {
                Text(label)
                    .font(.caption)
                    .foregroundColor(.secondary)
                Text(value)
                    .font(.subheadline)
                    .fontWeight(.medium)
            }

            Spacer()
        }
        .padding(10)
        .background(Color(.systemGray6))
        .cornerRadius(10)
    }
}

struct RequirementRow: View {
    let icon: String
    let text: String

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .font(.system(size: 16))
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 24)

            Text(text)
                .font(.subheadline)
                .foregroundColor(.secondary)

            Spacer()
        }
    }
}

// MARK: - Conditional coach-mark target helper

private extension View {
    /// Apply `.onboardingTarget(id)` only when `condition` is true, so a list
    /// can spotlight a single element without every row overwriting the anchor.
    @ViewBuilder
    func onboardingTargetIf(_ condition: Bool, _ id: TourTargetID) -> some View {
        if condition { self.onboardingTarget(id) } else { self }
    }
}

// MARK: - Preview

#Preview {
    DiscoverView()
        .environmentObject(AuthStore.shared)
        .environmentObject(LikedListingsStore.shared)
}
