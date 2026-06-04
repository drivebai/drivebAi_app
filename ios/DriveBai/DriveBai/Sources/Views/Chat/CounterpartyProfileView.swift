import SwiftUI

struct CounterpartyProfileView: View {
    let userId: UUID

    @StateObject private var viewModel: CounterpartyProfileViewModel

    init(userId: UUID) {
        self.userId = userId
        _viewModel = StateObject(wrappedValue: CounterpartyProfileViewModel(userId: userId))
    }

    var body: some View {
        Group {
            if viewModel.isLoading && viewModel.profile == nil {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let profile = viewModel.profile {
                profileContent(profile)
            } else if let error = viewModel.error {
                VStack(spacing: 12) {
                    Image(systemName: "exclamationmark.triangle")
                        .font(.largeTitle)
                        .foregroundColor(.secondary)
                    Text(error)
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                    Button("Retry") {
                        Task { await viewModel.loadProfile() }
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .navigationTitle("Profile")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            await viewModel.loadProfile()
        }
    }

    private func profileContent(_ profile: CounterpartyProfile) -> some View {
        ScrollView {
            VStack(spacing: 24) {
                // Avatar + name
                VStack(spacing: 12) {
                    if let url = ImageURLHelper.fullURL(for: profile.avatarURL) {
                        RemoteImage(url: url, maxPixelSize: 400)
                            .frame(width: 100, height: 100)
                            .clipShape(Circle())
                    } else {
                        Circle()
                            .fill(Color(.systemGray5))
                            .frame(width: 100, height: 100)
                            .overlay(
                                Image(systemName: "person.fill")
                                    .font(.system(size: 40))
                                    .foregroundColor(.gray)
                            )
                    }

                    Text(profile.fullName)
                        .font(.title2.weight(.semibold))

                    Text(profile.role.displayName)
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .padding(.horizontal, 12)
                        .padding(.vertical, 4)
                        .background(Color(.systemGray6))
                        .clipShape(Capsule())

                    Text("Member since \(formatDate(profile.memberSince))")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                .padding(.top, 16)

                Divider()
                    .padding(.horizontal)

                // Contact info
                if let phone = profile.phone {
                    infoRow(icon: "phone.fill", title: "Phone", value: phone)
                }

                // Role-specific sections
                switch profile.role {
                case .driver:
                    driverSection(profile)
                case .carOwner:
                    ownerSection(profile)
                case .admin:
                    EmptyView()
                }

                Spacer()
            }
        }
    }

    @ViewBuilder
    private func driverSection(_ profile: CounterpartyProfile) -> some View {
        VStack(alignment: .leading, spacing: 16) {
            sectionHeader("Driver Information")

            if let years = profile.yearsLicensed {
                infoRow(icon: "car.fill", title: "Years Licensed", value: "\(years) years")
            }

            if let trips = profile.totalTrips {
                infoRow(icon: "map.fill", title: "Total Trips", value: "\(trips)")
            }

            if profile.licenseDocumentURL != nil {
                HStack(spacing: 8) {
                    Image(systemName: "checkmark.seal.fill")
                        .foregroundColor(.green)
                    Text("License verified")
                        .font(.subheadline)
                        .foregroundColor(.green)
                }
                .padding(.horizontal)
            }
        }
    }

    @ViewBuilder
    private func ownerSection(_ profile: CounterpartyProfile) -> some View {
        VStack(alignment: .leading, spacing: 16) {
            sectionHeader("Owner Information")

            if let listings = profile.totalListings {
                infoRow(icon: "car.2.fill", title: "Total Listings", value: "\(listings)")
            }

            if let mechanicName = profile.mechanicName {
                infoRow(icon: "wrench.and.screwdriver.fill", title: "Mechanic", value: mechanicName)
            }

            if let mechanicPhone = profile.mechanicPhone {
                infoRow(icon: "phone.fill", title: "Mechanic Phone", value: mechanicPhone)
            }
        }
    }

    private func sectionHeader(_ title: String) -> some View {
        Text(title)
            .font(.headline)
            .padding(.horizontal)
    }

    private func infoRow(icon: String, title: String, value: String) -> some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 24)
            VStack(alignment: .leading, spacing: 1) {
                Text(title)
                    .font(.caption)
                    .foregroundColor(.secondary)
                Text(value)
                    .font(.subheadline)
            }
            Spacer()
        }
        .padding(.horizontal)
    }

    private func formatDate(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "MMMM yyyy"
        return formatter.string(from: date)
    }
}
