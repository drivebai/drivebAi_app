import SwiftUI

struct ProfileView: View {
    @EnvironmentObject private var authStore: AuthStore
    @Binding var showAuthFlow: Bool

    @State private var showLogoutConfirmation = false

    var body: some View {
        NavigationStack {
            Group {
                if let user = authStore.state.user {
                    AuthenticatedProfileView(
                        user: user,
                        showLogoutConfirmation: $showLogoutConfirmation
                    )
                } else {
                    UnauthenticatedProfileView(showAuthFlow: $showAuthFlow)
                }
            }
            .navigationTitle("Profile")
            .navigationBarTitleDisplayMode(.large)
        }
        .confirmationDialog(
            "Are you sure you want to log out?",
            isPresented: $showLogoutConfirmation,
            titleVisibility: .visible
        ) {
            Button("Log out", role: .destructive) {
                Task {
                    await authStore.logout()
                }
            }
            Button("Cancel", role: .cancel) {}
        }
    }
}

// MARK: - Authenticated Profile View

struct AuthenticatedProfileView: View {
    let user: UserProfile
    @Binding var showLogoutConfirmation: Bool

    /// Constructs the full URL for the profile photo
    private var profilePhotoURL: URL? {
        guard let photoPath = user.profilePhotoURL, !photoPath.isEmpty else {
            return nil
        }
        // Build full URL from base API URL and photo path
        let baseURL = "http://localhost:8080"
        return URL(string: baseURL + photoPath)
    }

    var body: some View {
        ScrollView {
            VStack(spacing: 24) {
                // Profile Header
                VStack(spacing: 16) {
                    // Avatar - shows photo if available, otherwise initials
                    if let photoURL = profilePhotoURL {
                        AsyncImage(url: photoURL) { phase in
                            switch phase {
                            case .empty:
                                ProgressView()
                                    .frame(width: 100, height: 100)
                            case .success(let image):
                                image
                                    .resizable()
                                    .scaledToFill()
                                    .frame(width: 100, height: 100)
                                    .clipShape(Circle())
                            case .failure:
                                // Fallback to initials on failure
                                profileInitialsView
                            @unknown default:
                                profileInitialsView
                            }
                        }
                    } else {
                        profileInitialsView
                    }

                    VStack(spacing: 4) {
                        Text(user.fullName)
                            .font(.title2)
                            .fontWeight(.bold)

                        Text(user.role.displayName)
                            .font(.subheadline)
                            .foregroundColor(.driveBaiPrimary)
                            .padding(.horizontal, 12)
                            .padding(.vertical, 4)
                            .background(
                                Capsule()
                                    .fill(Color.driveBaiPrimary.opacity(0.1))
                            )
                    }
                }
                .padding(.top, 24)

                // Profile Info
                VStack(spacing: 0) {
                    ProfileInfoRow(icon: "envelope.fill", title: "Email", value: user.email)
                    Divider().padding(.leading, 56)

                    if let phone = user.phone {
                        ProfileInfoRow(icon: "phone.fill", title: "Phone", value: phone)
                        Divider().padding(.leading, 56)
                    }

                    ProfileInfoRow(
                        icon: user.isEmailVerified ? "checkmark.seal.fill" : "exclamationmark.triangle.fill",
                        title: "Email Status",
                        value: user.isEmailVerified ? "Verified" : "Not Verified",
                        valueColor: user.isEmailVerified ? .green : .orange
                    )
                }
                .background(Color(.systemBackground))
                .cornerRadius(12)
                .padding(.horizontal)

                // Actions
                VStack(spacing: 0) {
                    ProfileActionRow(icon: "person.fill", title: "Edit Profile", action: {})
                    Divider().padding(.leading, 56)
                    ProfileActionRow(icon: "bell.fill", title: "Notifications", action: {})
                    Divider().padding(.leading, 56)
                    ProfileActionRow(icon: "lock.fill", title: "Privacy & Security", action: {})
                    Divider().padding(.leading, 56)
                    ProfileActionRow(icon: "questionmark.circle.fill", title: "Help & Support", action: {})
                }
                .background(Color(.systemBackground))
                .cornerRadius(12)
                .padding(.horizontal)

                // Logout Button
                Button(action: { showLogoutConfirmation = true }) {
                    HStack {
                        Image(systemName: "rectangle.portrait.and.arrow.right")
                        Text("Log out")
                    }
                    .foregroundColor(.red)
                }
                .padding(.top, 16)

                Spacer(minLength: 32)
            }
        }
        .background(Color(.systemGroupedBackground))
    }

    /// Fallback view showing user initials
    private var profileInitialsView: some View {
        Circle()
            .fill(Color.driveBaiPrimary.opacity(0.2))
            .frame(width: 100, height: 100)
            .overlay(
                Text(user.firstName.prefix(1).uppercased())
                    .font(.system(size: 40, weight: .bold))
                    .foregroundColor(.driveBaiPrimary)
            )
    }
}

// MARK: - Unauthenticated Profile View

struct UnauthenticatedProfileView: View {
    @Binding var showAuthFlow: Bool

    var body: some View {
        VStack(spacing: 32) {
            Spacer()

            VStack(spacing: 16) {
                Image(systemName: "person.crop.circle")
                    .font(.system(size: 80))
                    .foregroundColor(.gray)

                Text("Sign in to view your profile")
                    .font(.title3)
                    .fontWeight(.semibold)

                Text("Access your account, manage your listings, and more")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 32)
            }

            Button("Sign In") {
                showAuthFlow = true
            }
            .buttonStyle(DriveBaiButtonStyle())
            .padding(.horizontal, 32)

            Spacer()
        }
    }
}

// MARK: - Profile Info Row

struct ProfileInfoRow: View {
    let icon: String
    let title: String
    let value: String
    var valueColor: Color = .primary

    var body: some View {
        HStack(spacing: 16) {
            Image(systemName: icon)
                .font(.system(size: 20))
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 40)

            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.caption)
                    .foregroundColor(.secondary)
                Text(value)
                    .font(.subheadline)
                    .foregroundColor(valueColor)
            }

            Spacer()
        }
        .padding()
    }
}

// MARK: - Profile Action Row

struct ProfileActionRow: View {
    let icon: String
    let title: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 16) {
                Image(systemName: icon)
                    .font(.system(size: 20))
                    .foregroundColor(.driveBaiPrimary)
                    .frame(width: 40)

                Text(title)
                    .font(.subheadline)
                    .foregroundColor(.primary)

                Spacer()

                Image(systemName: "chevron.right")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            .padding()
        }
    }
}

#Preview {
    ProfileView(showAuthFlow: .constant(false))
        .environmentObject(AuthStore.shared)
}
