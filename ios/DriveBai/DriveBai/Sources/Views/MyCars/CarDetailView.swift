import SwiftUI

/// Design constants for car detail hero image
enum CarDetailUI {
    static let heroHeight: CGFloat = 300
    static let heroBackground = Color(.systemGray6)
}

/// Car detail screen with View/Edit toggle - Figma accurate
struct CarDetailView: View {
    let carId: UUID
    @StateObject private var store = OwnerCarsStore.shared
    @State private var isEditMode: Bool = false
    @Environment(\.dismiss) private var dismiss

    private var car: Car? {
        store.getCar(id: carId)
    }

    var body: some View {
        Group {
            if let car = car {
                if isEditMode {
                    CarDetailEditView(car: car, isEditMode: $isEditMode)
                } else {
                    CarDetailViewMode(car: car, isEditMode: $isEditMode)
                }
            } else {
                ContentUnavailableView("Car not found", systemImage: "car.fill")
            }
        }
        .navigationBarBackButtonHidden(isEditMode)
        .toolbar {
            if isEditMode {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button("Cancel") {
                        isEditMode = false
                    }
                }
            }
        }
    }
}

// MARK: - View Mode

struct CarDetailViewMode: View {
    let car: Car
    @Binding var isEditMode: Bool
    @State private var showEditLocation = false

    var body: some View {
        ZStack(alignment: .bottom) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    // Hero image
                    CarHeroImage(car: car)

                    // Content
                    VStack(alignment: .leading, spacing: 20) {
                        // Status badge + Title + Location + Locator button
                        HStack(alignment: .top) {
                            CarTitleSection(car: car)
                            Spacer()
                            Button(action: { showEditLocation = true }) {
                                Image(systemName: "location.fill")
                                    .font(.system(size: 14))
                                    .foregroundColor(.driveBaiPrimary)
                                    .frame(width: 36, height: 36)
                                    .background(Color.driveBaiPrimary.opacity(0.1))
                                    .clipShape(Circle())
                            }
                        }

                        // Price cards
                        CarPriceCards(car: car)

                        // Description
                        CarDescriptionSection(description: car.description)

                        // Requirements
                        CarRequirementsRow(requirements: car.requirements)

                        // Specs
                        CarSpecsSection(specs: car.specs)

                        // Owner info
                        CarOwnerSection(owner: car.owner)

                        // Bottom spacing for toggle
                        Spacer()
                            .frame(height: 80)
                    }
                    .padding(.horizontal, 16)
                    .padding(.top, 16)
                }
            }

            // Bottom View/Edit toggle
            ViewEditToggle(isEditMode: $isEditMode)
                .padding(.horizontal, 16)
                .padding(.bottom, 16)
        }
        .background(Color(.systemBackground))
        .navigationBarTitleDisplayMode(.inline)
        .fullScreenCover(isPresented: $showEditLocation) {
            OwnerEditCarLocationView(car: car)
        }
    }
}

// MARK: - Hero Image with Swipe Gallery

private struct CarHeroImage: View {
    let car: Car
    @State private var currentPage = 0

    /// Photo slots that have images (either local data or URL)
    private var photosWithImages: [CarPhotoSlot] {
        car.photoSlots.filter { $0.hasImage }.sorted { $0.slotType.sortOrder < $1.slotType.sortOrder }
    }

    var body: some View {
        ZStack(alignment: .top) {
            // Neutral background for fitted-image letterboxing
            CarDetailUI.heroBackground

            // Image gallery or placeholder
            if photosWithImages.isEmpty {
                placeholderView
            } else {
                TabView(selection: $currentPage) {
                    ForEach(Array(photosWithImages.enumerated()), id: \.element.id) { index, slot in
                        photoView(for: slot)
                            .tag(index)
                    }
                }
                .tabViewStyle(.page(indexDisplayMode: .never))
            }

            // Overlay: Status badge (top-left) and page indicator (bottom-center)
            VStack {
                HStack {
                    statusBadge
                    Spacer()
                }
                .padding(16)

                Spacer()

                if photosWithImages.count > 1 {
                    pageIndicator
                        .padding(.bottom, 12)
                }
            }
        }
        .frame(height: CarDetailUI.heroHeight)
    }

    private var placeholderView: some View {
        Rectangle()
            .fill(Color.driveBaiPrimary.opacity(0.1))
            .overlay(
                Image(systemName: "car.fill")
                    .font(.system(size: 60))
                    .foregroundColor(Color.driveBaiPrimary.opacity(0.3))
            )
    }

    @ViewBuilder
    private func photoView(for slot: CarPhotoSlot) -> some View {
        if let data = slot.localImageData, let uiImage = UIImage(data: data) {
            Image(uiImage: uiImage)
                .resizable()
                .scaledToFit()
        } else if let fullURL = slot.fullImageURL {
            RemoteImage(url: fullURL, contentMode: .fit)
        } else {
            placeholderView
        }
    }

    /// Hero status badge — THE canonical pill (QA pt 3). Derivation goes
    /// through CarBusinessState so this can never disagree with the My Cars
    /// card (no more "Available now!" while a rental is running).
    private var statusBadge: some View {
        CarStatusPill(state: .forCar(car), style: .hero)
    }

    private var pageIndicator: some View {
        HStack(spacing: 6) {
            ForEach(0..<photosWithImages.count, id: \.self) { index in
                Circle()
                    .fill(index == currentPage ? Color.white : Color.white.opacity(0.5))
                    .frame(width: 8, height: 8)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(Color.black.opacity(0.3))
        .cornerRadius(12)
    }
}

// MARK: - Title Section

private struct CarTitleSection: View {
    let car: Car

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(car.title)
                .font(.title2)
                .fontWeight(.bold)
                .foregroundColor(.primary)

            HStack(spacing: 4) {
                Image(systemName: "location.fill")
                    .font(.caption)
                    .foregroundColor(.secondary)

                Text(car.location.displayText)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            }
        }
    }
}

// MARK: - Price Cards

private struct CarPriceCards: View {
    let car: Car

    var body: some View {
        HStack(spacing: 12) {
            if car.isForRent, let rentPrice = car.weeklyRentPrice {
                PriceCard(
                    title: "Rent",
                    price: rentPrice.formatted,
                    subtitle: "/ week",
                    iconName: "key.fill",
                    isPrimary: true
                )
            }

            if car.isForSale, let salePrice = car.salePrice {
                PriceCard(
                    title: "Buy",
                    price: salePrice.formatted,
                    subtitle: nil,
                    iconName: "cart.fill",
                    isPrimary: !car.isForRent
                )
            }
        }
    }
}

private struct PriceCard: View {
    let title: String
    let price: String
    let subtitle: String?
    let iconName: String
    let isPrimary: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Image(systemName: iconName)
                    .font(.caption)
                    .foregroundColor(isPrimary ? .white : Color.driveBaiPrimary)

                Text(title)
                    .font(.caption)
                    .fontWeight(.medium)
                    .foregroundColor(isPrimary ? .white.opacity(0.8) : .secondary)
            }

            HStack(alignment: .firstTextBaseline, spacing: 2) {
                Text(price)
                    .font(.title3)
                    .fontWeight(.bold)
                    .foregroundColor(isPrimary ? .white : .primary)

                if let subtitle = subtitle {
                    Text(subtitle)
                        .font(.caption)
                        .foregroundColor(isPrimary ? .white.opacity(0.7) : .secondary)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .background(
            RoundedRectangle(cornerRadius: 12)
                .fill(isPrimary ? Color.driveBaiPrimary : Color(.systemGray6))
        )
    }
}

// MARK: - Description Section

private struct CarDescriptionSection: View {
    let description: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Description")
                .font(.headline)
                .foregroundColor(.primary)

            Text(description)
                .font(.subheadline)
                .foregroundColor(.secondary)
                .lineSpacing(4)
        }
    }
}

// MARK: - Requirements Row

private struct CarRequirementsRow: View {
    let requirements: CarRequirements

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Requirements")
                .font(.headline)
                .foregroundColor(.primary)

            // Deposit requirement removed (QA pt 7) — deposits are no longer
            // part of the product; only the licensing requirement remains.
            LazyVGrid(columns: [
                GridItem(.flexible()),
                GridItem(.flexible())
            ], spacing: 12) {
                RequirementItem(
                    icon: "person.text.rectangle",
                    text: requirements.licensingText
                )
            }
        }
    }
}

// MARK: - Specs Section

private struct CarSpecsSection: View {
    let specs: CarSpecs

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Specifications")
                .font(.headline)
                .foregroundColor(.primary)

            LazyVGrid(columns: [
                GridItem(.flexible()),
                GridItem(.flexible())
            ], spacing: 12) {
                SpecItem(label: "Body", value: specs.bodyType.displayText)
                SpecItem(label: "Fuel", value: specs.fuelType.rawValue)
                SpecItem(label: "Mileage", value: specs.mileageFormatted)
                SpecItem(label: "Year", value: "\(specs.year)")
            }
        }
    }
}

// MARK: - Spec Item

private struct SpecItem: View {
    let label: String
    let value: String

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.caption)
                .foregroundColor(.secondary)
            Text(value)
                .font(.subheadline)
                .fontWeight(.medium)
                .foregroundColor(.primary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(Color(.systemGray6))
        .cornerRadius(8)
    }
}

// MARK: - Requirement Item

private struct RequirementItem: View {
    let icon: String
    let text: String

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: icon)
                .font(.system(size: 16))
                .foregroundColor(Color.driveBaiPrimary)
                .frame(width: 32, height: 32)
                .background(Color.driveBaiPrimary.opacity(0.1))
                .cornerRadius(8)

            Text(text)
                .font(.subheadline)
                .foregroundColor(.secondary)
                .lineLimit(2)
                .minimumScaleFactor(0.9)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(Color(.systemGray6))
        .cornerRadius(8)
    }
}

// MARK: - Owner Section

private struct CarOwnerSection: View {
    let owner: CarOwnerInfo

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Owner")
                .font(.headline)
                .foregroundColor(.primary)

            HStack(spacing: 12) {
                // Avatar
                Circle()
                    .fill(Color.driveBaiPrimary.opacity(0.2))
                    .frame(width: 48, height: 48)
                    .overlay(
                        Text(owner.name.prefix(1))
                            .font(.title3)
                            .fontWeight(.semibold)
                            .foregroundColor(Color.driveBaiPrimary)
                    )

                // Name and rating
                VStack(alignment: .leading, spacing: 4) {
                    Text(owner.name)
                        .font(.subheadline)
                        .fontWeight(.semibold)
                        .foregroundColor(.primary)

                    HStack(spacing: 4) {
                        Image(systemName: "star.fill")
                            .font(.caption)
                            .foregroundColor(.yellow)

                        Text(owner.ratingFormatted)
                            .font(.caption)
                            .fontWeight(.medium)
                            .foregroundColor(.primary)

                        Text("(\(owner.reviewCount) reviews)")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }

                Spacer()

                // Chat CTA intentionally omitted: this screen is the owner's
                // management view of their own car. Chat is only meaningful
                // when viewing another user's listing (see ListingDetailView
                // in DiscoverView, gated by ChatEligibility.canStartChat).
            }
        }
    }
}

// MARK: - View/Edit Toggle

struct ViewEditToggle: View {
    @Binding var isEditMode: Bool

    var body: some View {
        HStack(spacing: 0) {
            ToggleButton(
                title: "View",
                isSelected: !isEditMode,
                action: { isEditMode = false }
            )

            ToggleButton(
                title: "Edit",
                isSelected: isEditMode,
                action: { isEditMode = true }
            )
        }
        .padding(4)
        .background(Color(.systemGray5))
        .cornerRadius(25)
    }
}

private struct ToggleButton: View {
    let title: String
    let isSelected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(title)
                .font(.subheadline)
                .fontWeight(.medium)
                .foregroundColor(isSelected ? .white : .primary)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 12)
                .background(
                    Capsule()
                        .fill(isSelected ? Color.driveBaiPrimary : Color.clear)
                )
        }
        .buttonStyle(PlainButtonStyle())
    }
}

#Preview {
    NavigationStack {
        CarDetailView(carId: OwnerCarsStore.shared.cars.first?.id ?? UUID())
    }
}
