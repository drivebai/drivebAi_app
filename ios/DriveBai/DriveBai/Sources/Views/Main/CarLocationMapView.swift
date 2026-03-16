import SwiftUI
import MapKit

/// Read-only car location map (for driver car detail "locator" button)
struct CarLocationMapView: View {
    let car: Car
    @Environment(\.dismiss) private var dismiss

    private var coordinate: CLLocationCoordinate2D {
        CLLocationCoordinate2D(latitude: car.location.latitude, longitude: car.location.longitude)
    }

    private var region: MKCoordinateRegion {
        MKCoordinateRegion(
            center: coordinate,
            span: MKCoordinateSpan(latitudeDelta: 0.01, longitudeDelta: 0.01)
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

            // Map
            Map(initialPosition: .region(region)) {
                Annotation(car.displayTitle, coordinate: coordinate) {
                    CarMapPin(isSelected: true)
                }
            }
            .mapStyle(.standard)
            .mapControls {
                MapCompass()
                MapScaleView()
            }

            // Bottom info
            VStack(spacing: 12) {
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
            .padding(16)
            .frame(maxWidth: .infinity)
            .background(Color(.systemBackground))
        }
        .navigationBarHidden(true)
    }
}
