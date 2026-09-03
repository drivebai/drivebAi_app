import SafariServices
import SwiftUI
import StripeConnect

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
    @State private var showSupportHub = false
    /// Standing "My documents" management sheet (F2): the decline
    /// notification tells the driver to re-upload, but there was no
    /// reachable surface without switching modes twice. This row is the
    /// permanent route in.
    @State private var showMyDocuments = false
    @State private var showEditProfile = false
    // Batch item 6: these rows were dead buttons (empty action closures
    // wearing chevrons). Now they open real screens.
    @State private var showNotificationSettings = false
    @State private var showPrivacySecurity = false
    @State private var showDeleteAccount = false
    /// Earnings & payouts (Stripe Connect): status + earnings for owners.
    /// The onboarding launch is feature-flagged until the SDK ships.
    @State private var showEarningsPayouts = false
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

                // Actions first, with Help & Support leading — the client
                // wants support reachable at the top, above the tour rows
                // (batch item 5). The tour section moved below.
                VStack(spacing: 0) {
                    ProfileActionRow(
                        icon: "questionmark.circle.fill",
                        title: "Help & Support",
                        badge: supportInboxStore.unreadCount,
                        action: { showSupportHub = true }
                    )
                    Divider().padding(.leading, 56)
                    ProfileActionRow(icon: "person.fill", title: "Edit Profile", action: { showEditProfile = true })
                    Divider().padding(.leading, 56)
                    ProfileActionRow(
                        icon: "doc.text.fill",
                        title: "My documents",
                        badge: rejectedDocumentsCount,
                        action: { showMyDocuments = true }
                    )
                    if user.role == .carOwner {
                        Divider().padding(.leading, 56)
                        ProfileActionRow(
                            icon: "banknote.fill",
                            title: "Earnings & payouts",
                            action: { showEarningsPayouts = true }
                        )
                    }
                    Divider().padding(.leading, 56)
                    ProfileActionRow(icon: "bell.fill", title: "Notifications", action: { showNotificationSettings = true })
                    Divider().padding(.leading, 56)
                    ProfileActionRow(icon: "lock.fill", title: "Privacy & Security", action: { showPrivacySecurity = true })
                }
                .background(Color(.systemBackground))
                .cornerRadius(12)
                .padding(.horizontal)

                // Help & product tour (moved below the actions — item 5)
                helpAndTipsSection

                // Logout Button
                Button(action: { showLogoutConfirmation = true }) {
                    HStack {
                        Image(systemName: "rectangle.portrait.and.arrow.right")
                        Text("Log out")
                    }
                    .foregroundColor(.red)
                }
                .padding(.top, 16)

                // Delete account (App Review 5.1.1(v)) — a REAL deletion,
                // not deactivation; two-step confirm inside the sheet.
                Button(action: { showDeleteAccount = true }) {
                    HStack {
                        Image(systemName: "trash")
                        Text("Delete account")
                    }
                    .foregroundColor(.red)
                }
                .padding(.top, 8)

                Spacer(minLength: 32)
            }
        }
        .background(Color(.systemGroupedBackground))
        // Keep documents fresh so the "My documents" rejected badge reflects
        // an admin decline without the user opening the sheet first.
        .task {
            await authStore.fetchDocuments()
        }
        .sheet(isPresented: $showDeleteAccount) {
            DeleteAccountSheet()
                .environmentObject(authStore)
        }
        .sheet(isPresented: $showEarningsPayouts) {
            EarningsPayoutsSheet()
        }
        .sheet(isPresented: $showSupportHub) {
            SupportHubView().environmentObject(supportInboxStore)
        }
        .sheet(isPresented: $showMyDocuments) {
            DriverDocsRequiredSheet(
                management: true,
                onCompleted: { showMyDocuments = false },
                onCancel: { showMyDocuments = false }
            )
            .environmentObject(authStore)
        }
        .sheet(isPresented: $showEditProfile) {
            EditProfileView(user: user)
                .environmentObject(authStore)
        }
        .sheet(isPresented: $showNotificationSettings) {
            NotificationSettingsView()
        }
        .sheet(isPresented: $showPrivacySecurity) {
            PrivacySecurityView(email: user.email)
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
    /// Red badge on "My documents" when any document was declined by admin —
    /// the visual cue that pairs with the decline push notification.
    private var rejectedDocumentsCount: Int {
        authStore.documents.filter { $0.status == .rejected }.count
    }

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

    /// Management mode (Profile → "My documents"): same cards, but framed as
    /// standing document management rather than a mode-switch gate — the
    /// bottom CTA is a plain Done and the copy explains the decline→re-upload
    /// loop instead of demanding documents "to continue".
    var management: Bool = false
    let onCompleted: () -> Void
    let onCancel: () -> Void

    // Dynamic card set (client point 1b): the old sheet hard-coded exactly
    // two cards, so a TLC uploaded at signup was permanently invisible here
    // while admin showed it Approved. Mirrors EmbeddedDocumentUploadContent
    // in SignupFlowView — same derivations, same add-another dialog.
    @State private var uploadingTypes: Set<DocumentType> = []
    @State private var addedOptionalTypes: Set<DocumentType> = []
    @State private var showAddOptionalSheet = false
    @State private var errorMessage: String?

    private func document(of type: DocumentType) -> Document? {
        authStore.documents.first { $0.type == type }
    }

    private var licenseDocument: Document? { document(of: .driversLicense) }

    /// Optional types with an upload on record or explicitly added this
    /// session — these render as cards.
    private var visibleOptionalTypes: [DocumentType] {
        DocumentType.optionalDriverDocs.filter { type in
            document(of: type) != nil || addedOptionalTypes.contains(type)
        }
    }

    /// The rest, offered through the "+ Add another document" dialog.
    private var addableOptionalTypes: [DocumentType] {
        DocumentType.optionalDriverDocs.filter { type in
            document(of: type) == nil && !addedOptionalTypes.contains(type)
        }
    }

    private var canContinue: Bool {
        // A rejected licence does NOT count (F2): the server excludes
        // rejected in HasRequiredDocuments, so counting it here just let the
        // user hit a confusing 409 after tapping Continue.
        guard let licenseDocument else { return false }
        return licenseDocument.status != .rejected
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    VStack(alignment: .leading, spacing: 8) {
                        Text(management ? "My documents" : "Driver documents required")
                            .font(.title2)
                            .fontWeight(.bold)
                        Text(management
                            ? "Your driver documents live here. If support declines one, you'll get a notification with the reason — upload a new copy below to restore it."
                            : "Driver's License is required to continue. Commercial License and other documents are optional and help with faster approval.")
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                    }
                    .padding(.horizontal)
                    .padding(.top, 8)

                    VStack(spacing: 16) {
                        // Required: the driver's licence, always visible.
                        DocumentUploadCard(
                            type: .driversLicense,
                            document: licenseDocument,
                            isUploading: uploadingTypes.contains(.driversLicense),
                            onFileSelected: { data, filename, mimeType in
                                Task { await upload(type: .driversLicense, data: data, filename: filename, mimeType: mimeType) }
                            },
                            onDelete: { delete(licenseDocument) },
                            remoteFileURL: licenseDocument?.fileUrl
                        )

                        // Optional slots: every type with an upload shows a
                        // card — including the TLC the client couldn't see.
                        ForEach(visibleOptionalTypes, id: \.self) { type in
                            DocumentUploadCard(
                                type: type,
                                document: document(of: type),
                                isUploading: uploadingTypes.contains(type),
                                onFileSelected: { data, filename, mimeType in
                                    Task { await upload(type: type, data: data, filename: filename, mimeType: mimeType) }
                                },
                                onDelete: { delete(document(of: type)) },
                                remoteFileURL: document(of: type)?.fileUrl
                            )
                        }

                        if !addableOptionalTypes.isEmpty {
                            Button {
                                showAddOptionalSheet = true
                            } label: {
                                HStack(spacing: 8) {
                                    Image(systemName: "plus.circle.fill")
                                    Text("Add another document")
                                }
                                .font(.subheadline.weight(.medium))
                                .foregroundColor(.driveBaiPrimary)
                                .frame(maxWidth: .infinity)
                                .padding(.vertical, 12)
                                .background(
                                    RoundedRectangle(cornerRadius: 12)
                                        .stroke(Color.driveBaiPrimary.opacity(0.35), style: StrokeStyle(lineWidth: 1.5, dash: [5]))
                                )
                            }
                        }
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
                    Text(management ? "Done" : "Continue to Driver mode")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(DriveBaiButtonStyle())
                .disabled(!management && !canContinue)
                .padding()
                .background(Color(.systemBackground))
            }
            .navigationTitle(management ? "My documents" : "Verify Driver")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { onCancel() }
                }
            }
            .confirmationDialog("Add a supporting document", isPresented: $showAddOptionalSheet, titleVisibility: .visible) {
                ForEach(addableOptionalTypes, id: \.self) { type in
                    Button(type.displayName) {
                        addedOptionalTypes.insert(type)
                    }
                }
            }
            .task {
                await authStore.fetchDocuments()
            }
        }
    }

    private func upload(type: DocumentType, data: Data, filename: String, mimeType: String) async {
        uploadingTypes.insert(type)
        defer { uploadingTypes.remove(type) }

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
            // If the user deletes an optional doc they just added, also clear
            // it from the visible-set so the "+ Add" picker offers it again
            // (mirrors EmbeddedDocumentUploadContent — without this, an empty
            // phantom card lingers for the rest of the sheet session).
            if document.type != .driversLicense {
                addedOptionalTypes.remove(document.type)
            }
        }
    }
}

// MARK: - Delete account (App Review 5.1.1(v))

/// Two-step, fully in-app account deletion. Step 1 states exactly what is
/// lost and that it is permanent; step 2 requires typing DELETE (plus the
/// password, for accounts that have one) before the destructive call. On
/// success the store clears the session and the app lands on the welcome
/// screen. Lives in this file deliberately — new Swift files need manual
/// pbxproj registration.
struct DeleteAccountSheet: View {
    @EnvironmentObject private var authStore: AuthStore
    @Environment(\.dismiss) private var dismiss

    @State private var confirming = false // step 1 → step 2
    @State private var typedConfirm = ""
    @State private var password = ""
    @State private var isDeleting = false
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    Label("This is permanent", systemImage: "exclamationmark.triangle.fill")
                        .font(.headline)
                        .foregroundColor(.red)

                    Text("Deleting your account:")
                        .font(.subheadline.weight(.semibold))
                    VStack(alignment: .leading, spacing: 8) {
                        bullet("Removes your profile, name, email, and phone number from DriveBai")
                        bullet("Permanently deletes your uploaded identity documents")
                        bullet("Removes your car listings from the marketplace")
                        bullet("Closes any open rental requests (the other party is notified)")
                        bullet("Signs you out everywhere — this cannot be undone")
                    }
                    Text("Rental and payment records are kept for bookkeeping, attributed to “Deleted User”. If you have an active rental or a payment in progress, you'll be asked to finish it first — every step can be completed right here in the app.")
                        .font(.footnote)
                        .foregroundColor(.secondary)

                    if confirming {
                        VStack(alignment: .leading, spacing: 12) {
                            Text("Type DELETE to confirm")
                                .font(.subheadline.weight(.semibold))
                            TextField("DELETE", text: $typedConfirm)
                                .textInputAutocapitalization(.characters)
                                .autocorrectionDisabled()
                                .textFieldStyle(.roundedBorder)

                            Text("Account password")
                                .font(.subheadline.weight(.semibold))
                            SecureField("Leave empty if you sign in with email codes", text: $password)
                                .textFieldStyle(.roundedBorder)
                        }
                        .padding(.top, 4)
                    }

                    if let errorMessage {
                        Text(errorMessage)
                            .font(.footnote)
                            .foregroundColor(.red)
                            .fixedSize(horizontal: false, vertical: true)
                    }

                    Button(action: primaryTapped) {
                        HStack {
                            if isDeleting { ProgressView().tint(.white) }
                            Text(confirming ? "Delete my account" : "Continue")
                                .font(.headline)
                        }
                        .foregroundColor(.white)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 14)
                        .background(canSubmit ? Color.red : Color.red.opacity(0.4))
                        .cornerRadius(12)
                    }
                    .disabled(!canSubmit || isDeleting)

                    Button("Cancel") { dismiss() }
                        .frame(maxWidth: .infinity)
                        .disabled(isDeleting)
                }
                .padding(20)
            }
            .navigationTitle("Delete account")
            .navigationBarTitleDisplayMode(.inline)
        }
        .interactiveDismissDisabled(isDeleting)
    }

    private var canSubmit: Bool {
        !confirming || typedConfirm.trimmingCharacters(in: .whitespaces).uppercased() == "DELETE"
    }

    private func bullet(_ text: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Text("•")
            Text(text)
        }
        .font(.subheadline)
    }

    private func primaryTapped() {
        if !confirming {
            confirming = true
            return
        }
        isDeleting = true
        errorMessage = nil
        Task {
            do {
                try await authStore.deleteAccount(
                    confirm: typedConfirm.trimmingCharacters(in: .whitespaces),
                    password: password
                )
                // Session is cleared — the app is already on the welcome
                // screen underneath; just close the sheet.
                dismiss()
            } catch {
                errorMessage = error.localizedDescription
            }
            isDeleting = false
        }
    }
}

#Preview {
    ProfileView(showAuthFlow: .constant(false))
        .environmentObject(AuthStore.shared)
}

// MARK: - Notification Settings (batch item 6)

/// A REAL notifications screen, not a stub: reads the actual OS push
/// permission, offers the correct next action for each state (request
/// permission / open OS Settings), and says plainly what DriveBai sends.
/// Notification delivery itself is OS-level — there are no server-side
/// per-category toggles today, and this screen doesn't pretend there are.
struct NotificationSettingsView: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var scenePhase

    @State private var authStatus: UNAuthorizationStatus = .notDetermined

    var body: some View {
        NavigationStack {
            List {
                Section {
                    HStack {
                        Image(systemName: statusIcon)
                            .foregroundColor(statusColor)
                        VStack(alignment: .leading, spacing: 2) {
                            Text("Push notifications")
                                .font(.body.weight(.medium))
                            Text(statusText)
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                        Spacer()
                    }

                    if authStatus == .notDetermined {
                        Button("Enable notifications") {
                            Task { await requestPermission() }
                        }
                        .foregroundColor(.driveBaiPrimary)
                    } else if authStatus == .denied {
                        Button("Open Settings to enable") {
                            if let url = URL(string: UIApplication.openSettingsURLString) {
                                UIApplication.shared.open(url)
                            }
                        }
                        .foregroundColor(.driveBaiPrimary)
                    }
                } footer: {
                    Text("Delivery is controlled by iOS. Turning notifications off in Settings stops all DriveBai pushes.")
                }

                Section("What we send") {
                    notifRow("message.fill", "Messages", "New chat messages from owners, renters and buyers")
                    notifRow("key.fill", "Rentals", "Booking updates, pickup reminders and returns")
                    notifRow("dollarsign.circle.fill", "Buying & selling", "Offers, signatures and sale progress")
                    notifRow("doc.text.fill", "Documents", "Decisions on your license and vehicle paperwork")
                    notifRow("questionmark.circle.fill", "Support", "Replies from the DriveBai team")
                }
            }
            .navigationTitle("Notifications")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Close") { dismiss() } }
            }
            .task { await refreshStatus() }
            // Coming back from OS Settings re-reads the real state.
            .onChange(of: scenePhase) { _, phase in
                if phase == .active { Task { await refreshStatus() } }
            }
        }
    }

    private func notifRow(_ icon: String, _ title: String, _ detail: String) -> some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 26)
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.subheadline.weight(.medium))
                Text(detail).font(.caption).foregroundColor(.secondary)
            }
        }
        .padding(.vertical, 2)
    }

    private var statusText: String {
        switch authStatus {
        case .authorized, .provisional, .ephemeral: return "On — you'll get updates as they happen"
        case .denied: return "Off — enable them in Settings to get updates"
        case .notDetermined: return "Not set up yet"
        @unknown default: return "Unknown"
        }
    }

    private var statusIcon: String {
        switch authStatus {
        case .authorized, .provisional, .ephemeral: return "bell.badge.fill"
        case .denied: return "bell.slash.fill"
        default: return "bell"
        }
    }

    private var statusColor: Color {
        switch authStatus {
        case .authorized, .provisional, .ephemeral: return .driveBaiPrimary
        case .denied: return .orange
        default: return .secondary
        }
    }

    @MainActor
    private func refreshStatus() async {
        let settings = await UNUserNotificationCenter.current().notificationSettings()
        authStatus = settings.authorizationStatus
    }

    @MainActor
    private func requestPermission() async {
        _ = try? await UNUserNotificationCenter.current()
            .requestAuthorization(options: [.alert, .badge, .sound])
        await refreshStatus()
        if authStatus == .authorized {
            UIApplication.shared.registerForRemoteNotifications()
        }
    }
}

// MARK: - Privacy & Security (batch item 6)

/// A REAL privacy screen: password change via the existing reset-email flow
/// (reused, not rebuilt), the actual policy pages on drivebai.com, and an
/// honest note on how documents are stored. No invented toggles.
struct PrivacySecurityView: View {
    let email: String

    @Environment(\.dismiss) private var dismiss
    @State private var sendingReset = false
    @State private var resetSent = false
    @State private var resetError: String?

    var body: some View {
        NavigationStack {
            List {
                Section {
                    Button {
                        Task { await sendReset() }
                    } label: {
                        HStack {
                            Image(systemName: "key.fill")
                                .foregroundColor(.driveBaiPrimary)
                                .frame(width: 26)
                            VStack(alignment: .leading, spacing: 2) {
                                Text("Change password")
                                    .font(.body.weight(.medium))
                                    .foregroundColor(.primary)
                                Text(resetSent
                                     ? "Reset link sent to \(email)"
                                     : "We'll email you a secure reset link")
                                    .font(.caption)
                                    .foregroundColor(resetSent ? .driveBaiPrimary : .secondary)
                            }
                            Spacer()
                            if sendingReset { ProgressView() }
                        }
                    }
                    .disabled(sendingReset || resetSent)

                    if let resetError {
                        Text(resetError).font(.caption).foregroundColor(.red)
                    }
                } header: {
                    Text("Security")
                } footer: {
                    Text("Passwords are stored as one-way hashes — nobody at DriveBai can read yours.")
                }

                Section("Your data") {
                    privacyRow("doc.badge.ellipsis", "Documents stay private",
                               "Your license and vehicle paperwork are stored privately; links to them expire and are never public.")
                    privacyRow("mappin.slash", "Location is approximate until booking",
                               "Browsers see a car's neighborhood only — exact addresses unlock after sign-in with a booking.")
                }

                Section("Policies") {
                    Link(destination: URL(string: "https://drivebai.com/privacy")!) {
                        Label("Privacy Policy", systemImage: "hand.raised.fill")
                    }
                    Link(destination: URL(string: "https://drivebai.com/terms")!) {
                        Label("Terms of Service", systemImage: "doc.plaintext.fill")
                    }
                }
            }
            .navigationTitle("Privacy & Security")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Close") { dismiss() } }
            }
        }
    }

    private func privacyRow(_ icon: String, _ title: String, _ detail: String) -> some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 26)
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.subheadline.weight(.medium))
                Text(detail).font(.caption).foregroundColor(.secondary)
            }
        }
        .padding(.vertical, 2)
    }

    @MainActor
    private func sendReset() async {
        sendingReset = true
        resetError = nil
        defer { sendingReset = false }
        do {
            _ = try await APIClient.shared.forgotPassword(request: ForgotPasswordRequest(email: email))
            resetSent = true
        } catch {
            resetError = "Couldn't send the reset email. Please try again."
        }
    }
}

// MARK: - Earnings & payouts (Stripe Connect)

/// Presents Stripe's embedded account-onboarding component (StripeConnect,
/// iOS 15+; app targets 17.6). Owns the EmbeddedComponentManager for the
/// component's lifetime and forwards the two delegate callbacks back onto
/// the main actor. The AccountOnboardingController retains itself while
/// presented, so only the manager needs holding here.
@MainActor
final class PayoutOnboardingLauncher: NSObject, ObservableObject {
    @Published var isLaunching = false
    @Published var launchError: String?

    /// Called when the component closes for ANY reason — completed, exited
    /// early, or abandoned. The caller must re-fetch status from OUR
    /// backend; the client is never trusted to know the outcome.
    private var onExit: (() -> Void)?
    private var componentManager: EmbeddedComponentManager?

    func launch(onExit: @escaping () -> Void) {
        guard !isLaunching else { return }
        self.onExit = onExit
        isLaunching = true
        launchError = nil
        Task {
            defer { isLaunching = false }
            do {
                // First session fetch pins the publishable key before any
                // component web view loads (validateKey runs at load); the
                // manager's closure re-fetches fresh secrets on refresh.
                let session = try await APIClient.shared.createPayoutSession()
                STPAPIClient.shared.publishableKey = session.publishableKey
                let manager = EmbeddedComponentManager(
                    appearance: Self.driveBaiAppearance,
                    fetchClientSecret: {
                        (try? await APIClient.shared.createPayoutSession())?.clientSecret
                    }
                )
                componentManager = manager
                let controller = manager.createAccountOnboardingController()
                controller.delegate = self
                controller.title = "Payout setup"
                guard let presenter = Self.topViewController() else {
                    launchError = "Couldn't open payout setup. Please try again."
                    return
                }
                controller.present(from: presenter)
            } catch {
                launchError = "Couldn't start payout setup. Check your connection and try again."
            }
        }
    }

    /// The component styled to the house look: primary green on actions and
    /// accents, 12pt corners to match the app's cards. System font is the
    /// app's type, which is Appearance's default — no CustomFontSource
    /// needed.
    private static var driveBaiAppearance: EmbeddedComponentManager.Appearance {
        var a = EmbeddedComponentManager.Appearance()
        let primary = UIColor(Color.driveBaiPrimary)
        a.colors.primary = primary
        a.colors.formAccent = primary
        a.colors.actionPrimaryText = primary
        a.buttonPrimary.colorBackground = primary
        a.buttonPrimary.colorText = .white
        a.cornerRadius.base = 12
        a.cornerRadius.button = 12
        return a
    }

    private static func topViewController() -> UIViewController? {
        let root = UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .flatMap(\.windows)
            .first(where: \.isKeyWindow)?
            .rootViewController
        guard var top = root else { return nil }
        while let presented = top.presentedViewController { top = presented }
        return top
    }
}

extension PayoutOnboardingLauncher: AccountOnboardingControllerDelegate {
    nonisolated func accountOnboardingDidExit(_ accountOnboarding: AccountOnboardingController) {
        Task { @MainActor in self.onExit?() }
    }

    nonisolated func accountOnboarding(_ accountOnboarding: AccountOnboardingController, didFailLoadWithError error: Error) {
        Task { @MainActor in
            self.launchError = "Payout setup couldn't load. Check your connection and try again."
        }
    }
}

/// Owner-facing payout home: what "getting paid" means, where their account
/// stands, and every payout with its state. The backend (accounts, account
/// sessions, transfers) is fully live; the embedded onboarding LAUNCH is
/// feature-flagged off until the StripeConnect SDK ships in the next
/// update. Lives in this file deliberately — new Swift files need manual
/// pbxproj registration.
struct EarningsPayoutsSheet: View {
    @Environment(\.dismiss) private var dismiss

    @State private var account: PayoutAccount?
    @State private var payouts: [OwnerPayoutItem] = []
    @State private var isLoading = true
    @State private var errorMessage: String?
    @StateObject private var onboarding = PayoutOnboardingLauncher()
    /// True while re-fetching status right after the component closed —
    /// the moment the user most wants an accurate answer.
    @State private var isRefreshingAfterOnboarding = false
    /// Part A: one-time Express dashboard URL for bank management, shown
    /// in an in-app Safari sheet. Status re-fetches on dismiss — the
    /// client is never trusted to know what changed in there.
    @State private var dashboardURL: URL?
    @State private var isFetchingDashboardLink = false
    @State private var dashboardLinkError: String?

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    if isLoading {
                        HStack {
                            Spacer()
                            ProgressView("Loading your earnings…")
                                .padding(.vertical, 48)
                            Spacer()
                        }
                    } else if let errorMessage {
                        VStack(spacing: 12) {
                            Image(systemName: "wifi.exclamationmark")
                                .font(.largeTitle)
                                .foregroundColor(.secondary)
                            Text(errorMessage)
                                .font(.subheadline)
                                .foregroundColor(.secondary)
                                .multilineTextAlignment(.center)
                            Button("Try again") { Task { await load() } }
                                .buttonStyle(.bordered)
                        }
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 40)
                    } else {
                        statusCard
                        bankCard
                        earningsCard
                        historySection
                    }
                }
                .padding()
            }
            .background(Color(.systemGroupedBackground))
            .navigationTitle("Earnings & payouts")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Done") { dismiss() }
                }
            }
            .task { await load() }
        }
    }

    // MARK: Status

    private var status: PayoutAccountStatus { account?.status ?? .none }

    @ViewBuilder
    private var statusCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 10) {
                Image(systemName: statusIcon)
                    .font(.title2)
                    .foregroundColor(statusColor)
                Text(statusTitle)
                    .font(.headline)
            }
            Text(statusBody)
                .font(.subheadline)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            if status == .actionNeeded, let due = account?.currentlyDue, !due.isEmpty {
                Text("Stripe still needs \(due.count) item\(due.count == 1 ? "" : "s") from you.")
                    .font(.footnote)
                    .foregroundColor(.orange)
            }

            if status == .none || status == .onboarding || status == .actionNeeded {
                setupButton
            }
        }
        .padding()
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.systemBackground))
        .cornerRadius(12)
    }

    private var statusIcon: String {
        switch status {
        case .none:                return "banknote"
        case .onboarding:          return "person.crop.circle.badge.clock"
        case .pendingVerification: return "clock.badge.checkmark"
        case .actionNeeded:        return "exclamationmark.circle.fill"
        case .ready:               return "checkmark.seal.fill"
        case .restricted:          return "pause.circle.fill"
        }
    }

    private var statusColor: Color {
        switch status {
        case .ready:                          return .green
        case .actionNeeded, .restricted:      return .orange
        default:                              return .driveBaiPrimary
        }
    }

    private var statusTitle: String {
        switch status {
        case .none:                return "Get paid automatically"
        case .onboarding:          return "Finish setting up payouts"
        case .pendingVerification: return "Stripe is reviewing your details"
        case .actionNeeded:        return "One more thing is needed"
        case .ready:               return "You're set up"
        case .restricted:          return "Payouts are paused"
        }
    }

    private var statusBody: String {
        switch status {
        case .none:
            return "When a rental completes, your share is transferred straight to your bank account — no invoices, no chasing. Payouts run on Stripe, the same payment platform behind the rest of DriveBai: your identity check and bank details go directly to Stripe, and we never see them."
        case .onboarding:
            return "Your payout account was started but Stripe hasn't received all your details yet. Anything you earn in the meantime is held safely and transfers automatically the moment setup is complete."
        case .pendingVerification:
            return "Your details are submitted and Stripe is verifying them — this usually takes minutes, occasionally up to a couple of days. Your earnings are held safely and transfer automatically once verification finishes."
        case .actionNeeded:
            return "Stripe needs a little more information to keep your payouts running. Your earnings are held safely in the meantime."
        case .ready:
            return "Rental payouts go straight to your bank automatically when a rental completes. Nothing else to do."
        case .restricted:
            return "Stripe has paused payouts on your account. Rentals you complete are held safely until this is resolved — contact support and we'll help you sort it out."
        }
    }

    /// The launch button. Presents the embedded onboarding component; when
    /// it closes — completed, exited early, or abandoned — status is
    /// re-fetched from OUR endpoint (which live-refreshes from Stripe), so
    /// the card always tells the truth: ready, still reviewing, more
    /// needed, or pick-up-where-you-left-off.
    @ViewBuilder
    private var setupButton: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button {
                onboarding.launch {
                    Task { await refreshAfterOnboarding() }
                }
            } label: {
                HStack {
                    if onboarding.isLaunching || isRefreshingAfterOnboarding {
                        ProgressView().controlSize(.small)
                    }
                    Text(isRefreshingAfterOnboarding ? "Checking your status…"
                         : status == .none ? "Set up payouts" : "Continue setup")
                        .font(.subheadline.weight(.semibold))
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 12)
            }
            .buttonStyle(.borderedProminent)
            .tint(.driveBaiPrimary)
            .disabled(!AppConfig.payoutOnboardingEnabled || onboarding.isLaunching || isRefreshingAfterOnboarding)

            if let launchError = onboarding.launchError {
                Text(launchError)
                    .font(.footnote)
                    .foregroundColor(.orange)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if !AppConfig.payoutOnboardingEnabled {
                Text("Payout setup will be available in the next update. Everything you earn until then is safely held and pays out the moment you're set up.")
                    .font(.footnote)
                    .foregroundColor(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    /// Server-truth refresh after the component closes. The GET endpoint
    /// reads the live account from Stripe and updates our mirror, so
    /// whatever the user did in there — finished, bailed, got queued for
    /// review, was asked for more documents — the status card lands on the
    /// accurate state with its own actionable copy.
    @MainActor
    private func refreshAfterOnboarding() async {
        isRefreshingAfterOnboarding = true
        await load()
        isRefreshingAfterOnboarding = false
    }

    // MARK: Bank account & payout schedule (Part A)

    /// Shown only when payouts are READY — a not-yet-onboarded owner has
    /// no bank to manage and keeps the onboarding CTA instead (never a
    /// dead end). Managing opens Stripe's Express dashboard in an in-app
    /// Safari sheet: the supported bank-editing surface for our account
    /// configuration. Stripe texts a sign-in code first — expected, and
    /// the copy says so.
    @ViewBuilder
    private var bankCard: some View {
        if status == .ready {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 10) {
                    Image(systemName: "building.columns.fill")
                        .font(.title3)
                        .foregroundColor(.driveBaiPrimary)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(bankLine)
                            .font(.subheadline.weight(.semibold))
                        if let schedule = account?.payoutSchedule {
                            Text(scheduleLine(schedule))
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                    }
                    Spacer()
                }

                Button {
                    openDashboard()
                } label: {
                    HStack {
                        if isFetchingDashboardLink {
                            ProgressView().controlSize(.small)
                        }
                        Text("Manage bank account")
                            .font(.subheadline.weight(.semibold))
                    }
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 12)
                }
                .buttonStyle(.bordered)
                .tint(.driveBaiPrimary)
                .disabled(isFetchingDashboardLink)

                if let dashboardLinkError {
                    Text(dashboardLinkError)
                        .font(.footnote)
                        .foregroundColor(.orange)
                }

                Text("Opens your secure Stripe dashboard. Stripe will text a sign-in code to your phone first — that's their standard protection for bank details, not a problem with your account.")
                    .font(.footnote)
                    .foregroundColor(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding()
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(.systemBackground))
            .cornerRadius(12)
            .sheet(item: $dashboardURL) { url in
                PayoutSafariView(url: url)
                    .ignoresSafeArea()
                    .onDisappear {
                        // Never trust the client: whatever happened in the
                        // dashboard, re-read status/bank from our endpoint.
                        Task { await load() }
                    }
            }
        }
    }

    private var bankLine: String {
        let name = account?.bankName?.isEmpty == false ? account!.bankName! : "Bank account"
        if let last4 = account?.bankLast4, !last4.isEmpty {
            return "\(name) •••• \(last4)"
        }
        return name
    }

    private func scheduleLine(_ schedule: PayoutSchedule) -> String {
        let cadence: String
        switch schedule.interval {
        case "daily": cadence = "daily"
        case "weekly": cadence = "weekly"
        case "monthly": cadence = "monthly"
        case "manual": cadence = "manual"
        default: cadence = schedule.interval
        }
        if schedule.delayDays > 0 {
            return "Payouts \(cadence) · \(schedule.delayDays)-day rolling delay"
        }
        return "Payouts \(cadence)"
    }

    private func openDashboard() {
        guard !isFetchingDashboardLink else { return }
        isFetchingDashboardLink = true
        dashboardLinkError = nil
        Task { @MainActor in
            defer { isFetchingDashboardLink = false }
            do {
                let link = try await APIClient.shared.fetchPayoutDashboardLink()
                if let url = URL(string: link.url) {
                    dashboardURL = url
                } else {
                    dashboardLinkError = "Couldn't open payout settings. Try again in a moment."
                }
            } catch {
                dashboardLinkError = "Couldn't open payout settings. Check your connection and try again."
            }
        }
    }

    // MARK: Earnings

    @ViewBuilder
    private var earningsCard: some View {
        let paid = account?.earnings?.paidCents ?? 0
        let awaiting = account?.earnings?.awaitingCents ?? 0
        let pending = account?.earnings?.pendingCents ?? 0
        if paid > 0 || awaiting > 0 || pending > 0 {
            VStack(spacing: 0) {
                earningsRow("Paid to you", cents: paid, color: .green)
                if pending > 0 {
                    Divider().padding(.leading, 16)
                    earningsRow("Sending", cents: pending, color: .driveBaiPrimary)
                }
                if awaiting > 0 {
                    Divider().padding(.leading, 16)
                    earningsRow("Awaiting setup", cents: awaiting, color: .orange)
                }
            }
            .background(Color(.systemBackground))
            .cornerRadius(12)
        }
    }

    private func earningsRow(_ title: String, cents: Int, color: Color) -> some View {
        HStack {
            Text(title).font(.subheadline)
            Spacer()
            Text(Self.money(cents))
                .font(.subheadline.weight(.semibold))
                .foregroundColor(color)
        }
        .padding()
    }

    // MARK: History

    @ViewBuilder
    private var historySection: some View {
        if !payouts.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                Text("Payout history")
                    .font(.headline)
                    .padding(.horizontal, 4)
                VStack(spacing: 0) {
                    ForEach(Array(payouts.enumerated()), id: \.element.id) { index, payout in
                        if index > 0 { Divider().padding(.leading, 16) }
                        payoutRow(payout)
                    }
                }
                .background(Color(.systemBackground))
                .cornerRadius(12)
            }
        }
    }

    private func payoutRow(_ payout: OwnerPayoutItem) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(Self.money(payout.ownerAmountCents))
                        .font(.subheadline.weight(.semibold))
                    Text(String(payout.createdAt.prefix(10)))
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                Spacer()
                Text(payoutStatusLabel(payout.status))
                    .font(.caption.weight(.medium))
                    .foregroundColor(payoutStatusColor(payout.status))
            }
            // A withheld payout is a deliberate decision, not a delay — the
            // recorded reason must be visible, or "withheld" reads as
            // "arriving soon" (client item 4).
            if payout.status == "withheld", let note = payout.note, !note.isEmpty {
                Text(note)
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding()
    }

    /// Each ledger status says exactly what it means — no euphemisms.
    /// "withheld" is a decision (reason shown above), not a hold.
    private func payoutStatusLabel(_ status: String) -> String {
        switch status {
        case "paid":                return "Paid"
        case "pending":             return "Sending"
        case "failed":              return "Failed — retrying"
        case "awaiting_onboarding": return "Awaiting setup"
        case "withheld":            return "Withheld"
        default:                    return status
        }
    }

    private func payoutStatusColor(_ status: String) -> Color {
        switch status {
        case "paid":                return .green
        case "failed":              return .orange
        case "awaiting_onboarding": return .orange
        case "withheld":            return .secondary
        default:                    return .driveBaiPrimary
        }
    }

    // MARK: Data

    @MainActor
    private func load() async {
        isLoading = true
        errorMessage = nil
        do {
            async let accountReq = APIClient.shared.fetchPayoutAccount()
            async let payoutsReq = APIClient.shared.fetchOwnerPayouts()
            let (acct, history) = try await (accountReq, payoutsReq)
            account = acct
            payouts = history.payouts ?? []
        } catch {
            errorMessage = "Couldn't load your earnings. Check your connection and try again."
        }
        isLoading = false
    }

    private static func money(_ cents: Int) -> String {
        String(format: "$%.2f", Double(cents) / 100.0)
    }
}


// URL as a sheet item for the dashboard link.
extension URL: @retroactive Identifiable {
    public var id: String { absoluteString }
}

/// Minimal in-app Safari host for the Stripe Express dashboard (Part A).
/// SFSafariViewController is the supported surface here — the native
/// account-management component is Stripe-internal SPI on iOS.
struct PayoutSafariView: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> SFSafariViewController {
        SFSafariViewController(url: url)
    }

    func updateUIViewController(_ uiViewController: SFSafariViewController, context: Context) {}
}
