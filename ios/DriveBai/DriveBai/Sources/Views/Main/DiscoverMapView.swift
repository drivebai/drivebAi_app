import SwiftUI
import MapKit

// MARK: - Discover Map View (full map with car pins)

struct DiscoverMapView: View {
    let listings: [Car]
    @Binding var selectedCar: Car?
    @State private var cameraPosition: MapCameraPosition = .automatic

    /// Only listings that have valid coordinates
    private var mappableListings: [Car] {
        listings.filter { $0.location.hasCoordinate }
    }

    var body: some View {
        ZStack(alignment: .bottom) {
            Map(position: $cameraPosition, selection: Binding(
                get: { selectedCar?.id },
                set: { newID in
                    selectedCar = mappableListings.first { $0.id == newID }
                }
            )) {
                ForEach(mappableListings) { car in
                    let coordinate = CLLocationCoordinate2D(
                        latitude: car.location.latitude,
                        longitude: car.location.longitude
                    )
                    // Guest mode: server-redacted locations are approximate
                    // (displaced center). Draw the 1 km area circle the
                    // displacement guarantees contains the true location —
                    // the marker inside is a soft area dot, not the precise
                    // car pin.
                    if car.location.isApproximate {
                        MapCircle(center: coordinate, radius: 1000)
                            .foregroundStyle(Color.driveBaiPrimary.opacity(0.15))
                            .stroke(Color.driveBaiPrimary.opacity(0.4), lineWidth: 1)
                    }
                    Annotation(car.displayTitle, coordinate: coordinate) {
                        Group {
                            if car.location.isApproximate {
                                ApproximateAreaMarker(isSelected: selectedCar?.id == car.id)
                            } else {
                                CarMapPin(isSelected: selectedCar?.id == car.id)
                            }
                        }
                        .onTapGesture {
                            withAnimation(.easeInOut(duration: 0.2)) {
                                selectedCar = car
                            }
                        }
                    }
                    .tag(car.id)
                }
            }
            .mapStyle(.standard)
            .mapControls {
                MapCompass()
                MapUserLocationButton()
            }
            .onTapGesture {
                // Dismiss preview card when tapping on map
                if selectedCar != nil {
                    withAnimation(.easeInOut(duration: 0.2)) {
                        selectedCar = nil
                    }
                }
            }

            // Preview card
            if let car = selectedCar {
                MapPreviewCard(car: car)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
                    .padding(.horizontal, 16)
                    .padding(.bottom, 16)
            }
        }
        .onAppear {
            fitToAnnotations()
        }
        .onChange(of: mappableListings.count) {
            fitToAnnotations()
        }
    }

    private func fitToAnnotations() {
        guard !mappableListings.isEmpty else { return }
        let coords = mappableListings.map {
            CLLocationCoordinate2D(latitude: $0.location.latitude, longitude: $0.location.longitude)
        }
        let region = MKCoordinateRegion(coordinates: coords)
        withAnimation {
            cameraPosition = .region(region)
        }
    }
}

// MARK: - Car Map Pin

struct CarMapPin: View {
    var isSelected: Bool = false

    var body: some View {
        ZStack {
            // Pin shape
            Image(systemName: "car.fill")
                .font(.system(size: 14))
                .foregroundColor(.white)
                .frame(width: 36, height: 36)
                .background(isSelected ? Color.driveBaiPrimary : Color.black.opacity(0.85))
                .clipShape(Circle())
                .shadow(color: .black.opacity(0.3), radius: 4, y: 2)
        }
        .scaleEffect(isSelected ? 1.2 : 1.0)
        .animation(.spring(response: 0.3, dampingFraction: 0.6), value: isSelected)
    }
}

// MARK: - Approximate Area Marker (guest mode)

/// Marker for a server-displaced (approximate) location: a soft translucent
/// dot that reads as "somewhere in this area", deliberately unlike the
/// precise CarMapPin. Sits at the center of the 1 km MapCircle.
struct ApproximateAreaMarker: View {
    var isSelected: Bool = false

    var body: some View {
        Image(systemName: "car.fill")
            .font(.system(size: 11))
            .foregroundColor(.driveBaiPrimary)
            .frame(width: 28, height: 28)
            .background(Color.driveBaiPrimary.opacity(isSelected ? 0.45 : 0.25))
            .clipShape(Circle())
            .overlay(Circle().stroke(Color.driveBaiPrimary.opacity(0.6), lineWidth: 1))
            .scaleEffect(isSelected ? 1.15 : 1.0)
            .animation(.spring(response: 0.3, dampingFraction: 0.6), value: isSelected)
    }
}

// MARK: - Map Preview Card (bottom sheet style)

struct MapPreviewCard: View {
    let car: Car
    @EnvironmentObject private var likedStore: LikedListingsStore
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var deepLinkRouter: DeepLinkRouter

    private var isLiked: Bool {
        likedStore.isLiked(car.id)
    }

    var body: some View {
        NavigationLink(value: car) {
            VStack(alignment: .leading, spacing: 0) {
                // Image
                ZStack(alignment: .topTrailing) {
                    carImageView
                        .frame(height: 140)
                        .clipped()

                    // Action icons
                    HStack(spacing: 8) {
                        CircleIconButton(icon: "square.and.arrow.up") {}
                        CircleIconButton(
                            icon: isLiked ? "heart.fill" : "heart",
                            tintColor: isLiked ? .red : .white
                        ) {
                            guard authStore.state.isAuthenticated else {
                                deepLinkRouter.promptGuestSignIn(
                                    "Sign in to save cars you love",
                                    intent: .like(car.id)
                                )
                                return
                            }
                            withAnimation(.spring(response: 0.3, dampingFraction: 0.6)) {
                                likedStore.toggleLike(car.id)
                            }
                        }
                    }
                    .padding(10)
                }
                .clipShape(RoundedRectangle(cornerRadius: 12))

                // Info
                VStack(alignment: .leading, spacing: 6) {
                    HStack {
                        Text(car.displayTitle)
                            .font(.subheadline)
                            .fontWeight(.semibold)
                            .foregroundColor(.primary)
                            .lineLimit(1)

                        if car.status == .available {
                            Text("Available now!")
                                .font(.caption2)
                                .fontWeight(.semibold)
                                .foregroundColor(.green)
                                .padding(.horizontal, 6)
                                .padding(.vertical, 2)
                                .background(Color.green.opacity(0.1))
                                .cornerRadius(4)
                        }
                    }

                    HStack {
                        if let price = car.weeklyRentPrice {
                            Text(price.formatted)
                                .font(.subheadline)
                                .fontWeight(.bold)
                                .foregroundColor(.primary)
                            +
                            Text(" per week")
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }

                        Spacer()

                        if car.location.distanceMiles > 0 {
                            Text(String(format: "%.1f mi away", car.location.distanceMiles))
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                    }

                    // Requirements pills
                    // Deposit pill removed (QA pt 7): deposits are gone from
                    // the product; the backend now always serves 0.
                    HStack(spacing: 12) {
                        RequirementPill(
                            icon: "calendar",
                            text: "\(car.requirements.minYearsLicensedDriving) years"
                        )
                    }
                }
                .padding(.horizontal, 4)
                .padding(.top, 8)
                .padding(.bottom, 4)
            }
            .padding(10)
            .background(Color(.systemBackground))
            .cornerRadius(16)
            .shadow(color: .black.opacity(0.12), radius: 12, x: 0, y: 4)
        }
        .buttonStyle(PlainButtonStyle())
    }

    @ViewBuilder
    private var carImageView: some View {
        let coverSlot = car.photoSlots.first { $0.slotType == .coverFront }

        if let imageURL = coverSlot?.fullImageURL {
            RemoteImage(url: imageURL, contentMode: .fill, maxPixelSize: 500)
        } else {
            Rectangle()
                .fill(Color(.systemGray5))
                .overlay(
                    Image(systemName: "car.fill")
                        .font(.system(size: 30))
                        .foregroundColor(.gray)
                )
        }
    }
}

// MARK: - MKCoordinateRegion Helper

extension MKCoordinateRegion {
    init(coordinates: [CLLocationCoordinate2D]) {
        guard !coordinates.isEmpty else {
            self = MKCoordinateRegion(
                center: CLLocationCoordinate2D(latitude: 0, longitude: 0),
                span: MKCoordinateSpan(latitudeDelta: 1, longitudeDelta: 1)
            )
            return
        }

        var minLat = coordinates[0].latitude
        var maxLat = coordinates[0].latitude
        var minLng = coordinates[0].longitude
        var maxLng = coordinates[0].longitude

        for coord in coordinates {
            minLat = min(minLat, coord.latitude)
            maxLat = max(maxLat, coord.latitude)
            minLng = min(minLng, coord.longitude)
            maxLng = max(maxLng, coord.longitude)
        }

        let center = CLLocationCoordinate2D(
            latitude: (minLat + maxLat) / 2,
            longitude: (minLng + maxLng) / 2
        )

        let span = MKCoordinateSpan(
            latitudeDelta: max((maxLat - minLat) * 1.3, 0.01),
            longitudeDelta: max((maxLng - minLng) * 1.3, 0.01)
        )

        self = MKCoordinateRegion(center: center, span: span)
    }
}
