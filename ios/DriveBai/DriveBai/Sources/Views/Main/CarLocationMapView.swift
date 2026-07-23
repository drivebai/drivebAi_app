import SwiftUI
import MapKit

/// Read-only car location map (for driver car detail "locator" button).
/// For an approximate (guest-mode, server-displaced) location it renders a
/// 1 km area circle instead of a pin, and the address line becomes a
/// sign-in conversion prompt.
struct CarLocationMapView: View {
    let car: Car
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var deepLinkRouter: DeepLinkRouter

    private var isApproximate: Bool {
        car.location.isApproximate
    }

    private var coordinate: CLLocationCoordinate2D {
        CLLocationCoordinate2D(latitude: car.location.latitude, longitude: car.location.longitude)
    }

    private var region: MKCoordinateRegion {
        MKCoordinateRegion(
            center: coordinate,
            // Wide enough to see the whole 1 km circle when approximate.
            span: isApproximate
                ? MKCoordinateSpan(latitudeDelta: 0.035, longitudeDelta: 0.035)
                : MKCoordinateSpan(latitudeDelta: 0.01, longitudeDelta: 0.01)
        )
    }

    var body: some View {
        VStack(spacing: 0) {
            // Navigation bar
            HStack {
                Button(action: { dismiss() }) {
                    Image(systemName: "chevron.left")
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundColor(.primary)
                }

                Spacer()

                Text(car.displayTitle)
                    .font(.headline)
                    .lineLimit(1)

                Spacer()

                // Spacer for balance
                Color.clear.frame(width: 24, height: 24)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)

            // Map — a circle, never a pin, for an approximate location: the
            // displaced center is NOT where the car lives, and a pin would
            // claim it is.
            Map(initialPosition: .region(region)) {
                if isApproximate {
                    MapCircle(center: coordinate, radius: 1000)
                        .foregroundStyle(Color.driveBaiPrimary.opacity(0.15))
                        .stroke(Color.driveBaiPrimary.opacity(0.4), lineWidth: 1.5)
                } else {
                    Annotation(car.displayTitle, coordinate: coordinate) {
                        CarMapPin(isSelected: true)
                    }
                }
            }
            .mapStyle(.standard)
            .mapControls {
                MapCompass()
                MapScaleView()
            }

            // Bottom info
            VStack(spacing: 12) {
                if isApproximate {
                    Text("Approximate area — the exact pickup location is shared after you sign in")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)

                    Text(car.location.displayArea)
                        .font(.body)
                        .fontWeight(.medium)
                        .foregroundColor(.primary)
                        .multilineTextAlignment(.center)

                    if !authStore.state.isAuthenticated {
                        Button {
                            deepLinkRouter.promptGuestSignIn(
                                "Sign in to see the exact pickup location",
                                intent: .viewExactLocation(car.id)
                            )
                        } label: {
                            Text("Sign in to see the exact location")
                                .font(.subheadline)
                                .fontWeight(.semibold)
                                .foregroundColor(.driveBaiPrimary)
                        }
                    }
                } else {
                    if !car.location.displayAddressLine.isEmpty {
                        Text("(precise address as identified on map)")
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                            .multilineTextAlignment(.center)
                    }

                    Text(car.location.displayAddressLine)
                        .font(.body)
                        .fontWeight(.medium)
                        .foregroundColor(.primary)
                        .multilineTextAlignment(.center)
                }
            }
            .padding(16)
            .frame(maxWidth: .infinity)
            .background(Color(.systemBackground))
        }
        .navigationBarHidden(true)
    }
}
