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

    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var supportInboxStore: SupportInboxStore
    @State private var isSwitchingMode = false
    @State private var showDriverDocsSheet = false
    @State private var shouldRetrySwitchAfterDocs = false
    @State private var switchError: String?
    @State private var showSupportChat = false
    @State private var showEditProfile = false
    /// Set when a replayed tip was armed for its own screen rather than played here.
    @State private var armedTourNotice: String?

    /// Constructs the full URL for the profile photo
    private var profilePhotoURL: URL? {
        guard let photoPath = user.profilePhotoURL, !photoPath.isEmpty else {
            return nil
        }
        return URL(string: AppConfig.serverBaseURL.absoluteString + photoPath)
    }

    /// The mode the user can switch TO (the one they're not currently in).
    /// Admins don't get a switch affordance.
    private var switchTargetRole: UserRole? {
        switch user.role {
        case .driver:    return .carOwner
        case .carOwner:  return .driver
        case .admin:     return nil
        }
    }

    private var switchLabel: String {
        switch switchTargetRole {
        case .driver:   return "Switch to Driver mode"
        case .carOwner: return "Switch to Owner mode"
        default:        return ""
        }
    }

    private var switchIcon: String {
        switch switchTargetRole {
        case .driver:   return "steeringwheel"
        case .carOwner: return "car.2.fill"
        default:        return "arrow.triangle.2.circlepath"
        }
    }

    var body: some View {
        ScrollView {
            VStack(spacing: 24) {
                // Profile Header
                VStack(spacing: 16) {
                    // Avatar - shows photo if available, otherwise initials
                    if let photoURL = profilePhotoURL {
                        RemoteImage(url: photoURL, contentMode: .fill, maxPixelSize: 300)
                            .frame(width: 100, height: 100)
                            .clipShape(Circle())
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

                // Mode switch (only when a valid target role exists)
                if switchTargetRole != nil {
                    VStack(spacing: 0) {
                        ProfileActionRow(
                            icon: switchIcon,
                            title: switchLabel,
                            isLoading: isSwitchingMode,
                            action: { performSwitch() }
                        )
                    }
                    .background(Color(.systemBackground))
                    .cornerRadius(12)
                    .padding(.horizontal)
                }

                // Help & product tour
                helpAndTipsSection

                // Actions
                VStack(spacing: 0) {
                    ProfileActionRow(icon: "person.fill", title: "Edit Profile", action: { showEditProfile = true })
                    Divider().padding(.leading, 56)
                    ProfileActionRow(icon: "bell.fill", title: "Notifications", action: {})
                    Divider().padding(.leading, 56)
                    ProfileActionRow(icon: "lock.fill", title: "Privacy & Security", action: {})
                    Divider().padding(.leading, 56)
                    ProfileActionRow(
                        icon: "questionmark.circle.fill",
                        title: "Contact Support",
                        badge: supportInboxStore.unreadCount,
                        action: { showSupportChat = true }
                    )
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
        .sheet(isPresented: $showSupportChat, onDismiss: {
            supportInboxStore.isSupportChatVisible = false
            Task { await supportInboxStore.markRead() }
        }) {
            SupportChatView()
                .environmentObject(authStore)
                .environmentObject(supportInboxStore)
        }
        .sheet(isPresented: $showEditProfile) {
            EditProfileView(user: user)
                .environmentObject(authStore)
        }
        .sheet(isPresented: $showDriverDocsSheet, onDismiss: {
            // Run the retry AFTER the sheet has fully dismissed and SwiftUI's
            // presentation transaction has settled. Doing this in the Continue
            // button's closure races with the dismiss transaction and can cause
            // the @Published state swap to .driver to be dropped, leaving the
            // user on OwnerTabView even though the network call succeeded.
            if shouldRetrySwitchAfterDocs {
                shouldRetrySwitchAfterDocs = false
                performSwitch(isRetryAfterDocs: true)
            }
        }) {
            DriverDocsRequiredSheet(
                onCompleted: {
                    // Mark intent and dismiss; the retry fires in onDismiss.
                    shouldRetrySwitchAfterDocs = true
                    showDriverDocsSheet = false
                },
                onCancel: {
                    shouldRetrySwitchAfterDocs = false
                    showDriverDocsSheet = false
                }
            )
            .environmentObject(authStore)
        }
        .alert("Couldn't switch mode", isPresented: Binding(
            get: { switchError != nil },
            set: { if !$0 { switchError = nil } }
        )) {
            Button("OK", role: .cancel) { switchError = nil }
        } message: {
            Text(switchError ?? "")
        }
        .alert("Tips are back on", isPresented: Binding(
            get: { armedTourNotice != nil },
            set: { if !$0 { armedTourNotice = nil } }
        )) {
            Button("OK", role: .cancel) { armedTourNotice = nil }
        } message: {
            Text(armedTourNotice ?? "")
        }
    }

    /// The product-tour role bucket for the signed-in user (admins share the
    /// owner surfaces, so they map to `.owner`).
    private var tourRole: TourRole {
        user.role == .driver ? .driver : .owner
    }

    /// Most tips spotlight a control that only exists on its own screen, so
    /// replaying one from Profile arms it rather than playing it here. Tell the
    /// user where to go instead of leaving the tap looking broken.
    private func handleReplay(_ outcome: TourReplayOutcome, screen: String) {
        guard outcome == .armedForLater else { return }
        armedTourNotice = "We'll walk you through it the next time you \(screen)."
    }

    /// "Help & product tour" — replay the tour, re-show the setup checklist, and
    /// re-run any individual tip. Every action just calls the coordinator; an
    /// explicit replay bypasses the new-user gate, so legacy users can opt in.
    private var helpAndTipsSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Help & product tour")
                .font(.footnote)
                .fontWeight(.semibold)
                .foregroundColor(.secondary)
                .padding(.horizontal)
                .padding(.leading, 4)

            VStack(spacing: 0) {
                ProfileActionRow(icon: "sparkles", title: "Replay app tour") {
                    handleReplay(ProductTourCoordinator.shared.replayWelcomeAndTabs(),
                                 screen: "the tour")
                }
                Divider().padding(.leading, 56)
                ProfileActionRow(icon: "checklist", title: "Show setup checklist") {
                    ProductTourCoordinator.shared.showChecklist(role: tourRole)
                }
                Divider().padding(.leading, 56)
                ProfileActionRow(icon: "bubble.left.and.bubble.right.fill",
                                 title: "How Messages & Requests work") {
                    handleReplay(ProductTourCoordinator.shared.replay(.chatSegments),
                                 screen: "open a chat")
                }
                Divider().padding(.leading, 56)
                if tourRole == .driver {
                    ProfileActionRow(icon: "car.fill", title: "Renting a car") {
                        handleReplay(ProductTourCoordinator.shared.replay(.driverFirstDiscover),
                                     screen: "open Discover")
                    }
                } else {
                    ProfileActionRow(icon: "car.fill", title: "Listing a car") {
                        handleReplay(ProductTourCoordinator.shared.replay(.ownerFirstListing),
                                     screen: "start adding a vehicle")
                    }
                }
                Divider().padding(.leading, 56)
                ProfileActionRow(icon: "creditcard.fill", title: "Buying or selling a car") {
                    handleReplay(ProductTourCoordinator.shared.replay(.purchaseIntro),
                                 screen: "open a car that's for sale")
                }

                #if DEBUG
                Divider().padding(.leading, 56)
                Button {
                    Task { await ProductTourCoordinator.shared.resetAll() }
                } label: {
                    HStack(spacing: 16) {
                        Image(systemName: "arrow.counterclockwise")
                            .font(.system(size: 20))
                            .foregroundColor(.red)
                            .frame(width: 40)
                        Text("Reset onboarding (this account)")
                            .font(.subheadline)
                            .foregroundColor(.red)
                        Spacer()
                    }
                    .padding()
                }
                #endif
            }
            .background(Color(.systemBackground))
            .cornerRadius(12)
            .padding(.horizontal)
        }
    }

    private func performSwitch(isRetryAfterDocs: Bool = false) {
        guard let target = switchTargetRole, !isSwitchingMode else { return }
        isSwitchingMode = true
        Task { @MainActor in
            do {
                let result = try await authStore.switchProfile(to: target)
                switch result {
                case .switched:
                    // ContentView will re-route to the new tab group automatically
                    // because it keys off `user.role` which /me now mirrors.
                    // Tell the tour so it can run the "you switched modes"
                    // explainer (and chain the new role's tab walk if eligible).
                    ProductTourCoordinator.shared.handle(
                        .roleSwitched(target == .driver ? .driver : .owner)
                    )
                    isSwitchingMode = false
                case .needsDriverDocs:
                    await authStore.fetchDocuments()
                    isSwitchingMode = false
                    if isRetryAfterDocs {
                        // The user just dismissed the docs sheet after a Continue
                        // tap, and the server still reports missing docs. Don't
                        // silently re-present (that strands them); surface a
                        // clear error so they understand the upload didn't take.
                        switchError = "We couldn't verify your driver documents. Please try again."
                    } else {
                        // Defer the present by one runloop tick — re-presenting
                        // the same binding inside the dismiss transaction can
                        // be dropped by SwiftUI.
                        Task { @MainActor in
                            showDriverDocsSheet = true
                        }
                    }
                }
            } catch let apiError as APIError {
                switchError = apiError.errorDescription ?? "Something went wrong."
                isSwitchingMode = false
            } catch {
                switchError = "Something went wrong. Please try again."
                isSwitchingMode = false
            }
        }
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
    var isLoading: Bool = false
    var badge: Int = 0
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

                if badge > 0 {
                    Text(badge > 99 ? "99+" : "\(badge)")
                        .font(.caption2.bold())
                        .foregroundColor(.white)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 3)
                        .background(Color.red)
                        .clipShape(Capsule())
                }

                if isLoading {
                    ProgressView()
                } else {
                    Image(systemName: "chevron.right")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
            }
            .padding()
        }
        .disabled(isLoading)
    }
}

// MARK: - Driver Documents Required Sheet
//
// Presented when the server rejects a switch-to-Driver with DRIVER_DOCS_REQUIRED.
// Reuses the existing DocumentUploadCard primitive so the UX matches the
// driver onboarding flow exactly. When both required documents are uploaded,
// the Continue button becomes enabled and calls `onCompleted`, which triggers
// the caller to retry the switch.

private struct DriverDocsRequiredSheet: View {
    @EnvironmentObject private var authStore: AuthStore
    @Environment(\.dismiss) private var dismiss

    let onCompleted: () -> Void
    let onCancel: () -> Void

    @State private var isUploadingLicense = false
    @State private var isUploadingCommercial = false
    @State private var errorMessage: String?

    private var licenseDocument: Document? {
        authStore.documents.first { $0.type == .driversLicense }
    }

    private var commercialLicenseDocument: Document? {
        authStore.documents.first { $0.type == .commercialLicense }
    }

    private var canContinue: Bool {
        licenseDocument != nil
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Driver documents required")
                            .font(.title2)
                            .fontWeight(.bold)
                        Text("Driver's License is required to continue. Commercial License and other documents are optional and help with faster approval.")
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                    }
                    .padding(.horizontal)
                    .padding(.top, 8)

                    VStack(spacing: 16) {
                        DocumentUploadCard(
                            type: .driversLicense,
                            document: licenseDocument,
                            isUploading: isUploadingLicense,
                            onFileSelected: { data, filename, mimeType in
                                Task { await upload(type: .driversLicense, data: data, filename: filename, mimeType: mimeType) }
                            },
                            onDelete: { delete(licenseDocument) }
                        )

                        DocumentUploadCard(
                            type: .commercialLicense,
                            document: commercialLicenseDocument,
                            isUploading: isUploadingCommercial,
                            onFileSelected: { data, filename, mimeType in
                                Task { await upload(type: .commercialLicense, data: data, filename: filename, mimeType: mimeType) }
                            },
                            onDelete: { delete(commercialLicenseDocument) }
                        )
                    }
                    .padding(.horizontal)

                    if let errorMessage {
                        Text(errorMessage)
                            .font(.footnote)
                            .foregroundColor(.red)
                            .padding(.horizontal)
                    }

                    VStack(alignment: .leading, spacing: 8) {
                        Label("Accepted formats: JPEG, PNG, PDF", systemImage: "info.circle")
                        Label("Maximum file size: 10MB", systemImage: "doc")
                    }
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .padding(.horizontal)
                    .padding(.top, 4)

                    Spacer(minLength: 24)
                }
            }
            .background(Color(.systemGroupedBackground))
            .safeAreaInset(edge: .bottom) {
                Button(action: onCompleted) {
                    Text("Continue to Driver mode")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(DriveBaiButtonStyle())
                .disabled(!canContinue)
                .padding()
                .background(Color(.systemBackground))
            }
            .navigationTitle("Verify Driver")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { onCancel() }
                }
            }
            .task {
                await authStore.fetchDocuments()
            }
        }
    }

    private func upload(type: DocumentType, data: Data, filename: String, mimeType: String) async {
        if type == .driversLicense {
            isUploadingLicense = true
        } else {
            isUploadingCommercial = true
        }
        defer {
            if type == .driversLicense {
                isUploadingLicense = false
            } else {
                isUploadingCommercial = false
            }
        }

        errorMessage = nil
        do {
            try await authStore.uploadDocument(
                type: type,
                fileData: data,
                filename: filename,
                mimeType: mimeType
            )
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription ?? "Failed to upload document"
        } catch {
            errorMessage = "Failed to upload document"
        }
    }

    private func delete(_ document: Document?) {
        guard let document else { return }
        Task {
            try? await authStore.deleteDocument(id: document.id)
        }
    }
}

#Preview {
    ProfileView(showAuthFlow: .constant(false))
        .environmentObject(AuthStore.shared)
}
