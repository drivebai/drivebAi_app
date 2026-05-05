import SwiftUI
import UIKit
import UserNotifications

// MARK: - AppDelegate (captures APNs token callbacks)

final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(
        _ application: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
    ) {
        let token = deviceToken.map { String(format: "%02x", $0) }.joined()
        NotificationCenter.default.post(name: .didRegisterDeviceToken, object: token)
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: Error
    ) {
        #if DEBUG
        print("[Push] Failed to register for remote notifications: \(error)")
        #endif
    }
}

@main
struct DriveBaiApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var authStore = AuthStore.shared
    @StateObject private var deepLinkRouter = DeepLinkRouter.shared
    @StateObject private var likedListingsStore = LikedListingsStore.shared

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(authStore)
                .environmentObject(deepLinkRouter)
                .environmentObject(likedListingsStore)
                .task {
                    await authStore.checkAuthState()
                    if authStore.state.isAuthenticated {
                        await likedListingsStore.fetchLikedListings()
                        await ChatsListViewModel.shared.fetchChats()
                        WebSocketManager.shared.connect()
                        await requestPushPermissionIfNeeded()
                    }
                }
                .onChange(of: authStore.state) { _, newState in
                    if newState.isAuthenticated {
                        WebSocketManager.shared.connect()
                        Task {
                            await ChatsListViewModel.shared.fetchChats()
                            await requestPushPermissionIfNeeded()
                        }
                    } else {
                        WebSocketManager.shared.disconnect()
                        ChatsListViewModel.shared.clearAll()
                    }
                }
                .onReceive(NotificationCenter.default.publisher(for: .didRegisterDeviceToken)) { note in
                    guard let token = note.object as? String else { return }
                    Task { _ = try? await APIClient.shared.registerDeviceToken(token: token, sandbox: AppConfig.apnsSandbox) }
                }
                .onOpenURL { url in
                    deepLinkRouter.handle(url: url)
                }
                .fullScreenCover(isPresented: $deepLinkRouter.showResetPassword) {
                    if let token = deepLinkRouter.resetPasswordToken {
                        NavigationStack {
                            ResetPasswordView(token: token)
                                .environmentObject(authStore)
                                .environmentObject(deepLinkRouter)
                        }
                    }
                }
                .alert("Error", isPresented: .constant(deepLinkRouter.deepLinkError != nil)) {
                    Button("OK") {
                        deepLinkRouter.clearError()
                    }
                } message: {
                    if let error = deepLinkRouter.deepLinkError {
                        Text(error)
                    }
                }
        }
    }
}

// MARK: - Push helpers

extension NSNotification.Name {
    static let didRegisterDeviceToken = NSNotification.Name("didRegisterDeviceToken")
}

/// Requests APNs permission on first run only (when status is .notDetermined).
/// On subsequent launches the OS re-registers silently via UIApplication.
@MainActor
private func requestPushPermissionIfNeeded() async {
    let center = UNUserNotificationCenter.current()
    let settings = await center.notificationSettings()
    guard settings.authorizationStatus == .notDetermined else {
        // Already authorized or denied — re-register silently so the token is current
        if settings.authorizationStatus == .authorized {
            UIApplication.shared.registerForRemoteNotifications()
        }
        return
    }
    do {
        let granted = try await center.requestAuthorization(options: [.alert, .sound, .badge])
        if granted {
            UIApplication.shared.registerForRemoteNotifications()
        }
    } catch {
        #if DEBUG
        print("[Push] requestAuthorization error: \(error)")
        #endif
    }
}

struct ContentView: View {
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var deepLinkRouter: DeepLinkRouter

    var body: some View {
        Group {
            switch authStore.state {
            case .unknown:
                // Loading state
                VStack {
                    ProgressView()
                        .scaleEffect(1.5)
                    Text("Loading...")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .padding(.top, 16)
                }

            case .unauthenticated:
                // OTP is the primary auth flow; password login is secondary
                EnterEmailOTPView(showDismissButton: false)
                    .environmentObject(deepLinkRouter)

            case .authenticated(let user):
                // Check if user needs onboarding
                if user.needsOnboarding {
                    // Show onboarding flow for users who need to complete steps
                    OnboardingResumeView(user: user)
                } else {
                    // Onboarding complete - show role-based navigation
                    switch user.role {
                    case .driver:
                        DriverTabView()
                    case .carOwner:
                        OwnerTabView()
                    case .admin:
                        // Admin gets owner view for now
                        OwnerTabView()
                    }
                }
            }
        }
        .animation(.easeInOut(duration: 0.3), value: authStore.state)
    }
}

// MARK: - Onboarding Resume View
/// Handles resuming onboarding for users who are authenticated but need to complete additional steps
/// This is shown when a user logs back in mid-onboarding

struct OnboardingResumeView: View {
    @EnvironmentObject private var authStore: AuthStore
    @StateObject private var signupFlow = SignupFlowState()

    let user: UserProfile

    var body: some View {
        NavigationStack {
            ZStack {
                Group {
                    switch signupFlow.currentStep {
                    case .userInfo, .role:
                        // Should not happen - user is already registered
                        // Fall through to profile photo
                        ResumeProfilePhotoStepView(onContinue: handleProfilePhotoContinue, onSkip: handleProfilePhotoSkip)
                            .transition(.asymmetric(insertion: .move(edge: .trailing), removal: .move(edge: .leading)))

                    case .profilePhoto:
                        ResumeProfilePhotoStepView(onContinue: handleProfilePhotoContinue, onSkip: handleProfilePhotoSkip)
                            .transition(.asymmetric(insertion: .move(edge: .trailing), removal: .move(edge: .leading)))

                    case .documents:
                        ResumeDocumentsStepView(onComplete: handleOnboardingComplete)
                            .transition(.asymmetric(insertion: .move(edge: .trailing), removal: .move(edge: .leading)))
                    }
                }
                .animation(.easeInOut(duration: 0.3), value: signupFlow.currentStep)
            }
        }
        .environmentObject(signupFlow)
        .onAppear {
            // Resume from where user left off
            signupFlow.resumeFromStatus(user.onboardingStatus, role: user.role)
        }
    }

    private func handleProfilePhotoContinue() {
        if user.role == .driver {
            signupFlow.goToNextStep()
        } else {
            completeOnboarding()
        }
    }

    private func handleProfilePhotoSkip() {
        handleProfilePhotoContinue()
    }

    private func completeOnboarding() {
        signupFlow.isLoading = true
        signupFlow.clearError()

        Task { @MainActor in
            defer { signupFlow.isLoading = false }
            do {
                try await authStore.completeOnboarding()
                // ContentView will switch to main tabs
            } catch {
                signupFlow.error = authStore.error
            }
        }
    }

    private func handleOnboardingComplete() {
        // ContentView will switch to main tabs automatically
    }
}

// MARK: - Resume Profile Photo Step

struct ResumeProfilePhotoStepView: View {
    @EnvironmentObject private var signupFlow: SignupFlowState
    @EnvironmentObject private var authStore: AuthStore

    let onContinue: () -> Void
    let onSkip: () -> Void

    @State private var selectedItem: PhotosPickerItem?
    @State private var isUploading = false

    var body: some View {
        SignupStepContainer(
            title: "Finish signing up",
            currentStep: signupFlow.currentStepIndex,
            totalSteps: signupFlow.totalSteps,
            showBackButton: false,
            isLoading: isUploading || signupFlow.isLoading,
            canContinue: true,
            continueTitle: signupFlow.hasProfilePhoto ? "Continue" : "Upload",
            showSkip: !signupFlow.hasProfilePhoto,
            onBack: nil,
            onContinue: { handleContinue() },
            onSkip: onSkip
        ) {
            VStack(spacing: 24) {
                SignupStepHeader(
                    title: "Add a profile photo",
                    subtitle: "This helps other users recognize you and builds trust in our community."
                )

                PhotosPicker(selection: $selectedItem, matching: .images) {
                    ZStack {
                        if let image = signupFlow.profilePhotoImage {
                            Image(uiImage: image)
                                .resizable()
                                .scaledToFill()
                                .frame(width: 160, height: 160)
                                .clipShape(Circle())
                        } else {
                            Circle()
                                .fill(Color(.systemGray5))
                                .frame(width: 160, height: 160)
                                .overlay(
                                    VStack(spacing: 8) {
                                        Image(systemName: "camera.fill")
                                            .font(.system(size: 40))
                                            .foregroundColor(.gray.opacity(0.5))
                                        Text("Tap to add")
                                            .font(.caption)
                                            .foregroundColor(.gray.opacity(0.5))
                                    }
                                )
                        }

                        if isUploading {
                            Circle()
                                .fill(Color.black.opacity(0.5))
                                .frame(width: 160, height: 160)
                                .overlay(
                                    ProgressView()
                                        .tint(.white)
                                        .scaleEffect(1.5)
                                )
                        }
                    }
                }
                .disabled(isUploading)

                if signupFlow.hasProfilePhoto {
                    PhotosPicker(selection: $selectedItem, matching: .images) {
                        Text("Change photo")
                            .font(.subheadline)
                            .foregroundColor(.driveBaiPrimary)
                    }
                    .disabled(isUploading)
                }

                if let error = signupFlow.error {
                    SignupErrorBanner(message: error)
                }
            }
            .padding(.top, 24)
        }
        .onChange(of: selectedItem) { _, newItem in
            if let item = newItem {
                Task {
                    await loadImage(from: item)
                    selectedItem = nil
                }
            }
        }
    }

    @MainActor
    private func loadImage(from item: PhotosPickerItem) async {
        isUploading = true
        signupFlow.clearError()
        defer { isUploading = false }

        do {
            guard let data = try await item.loadTransferable(type: Data.self) else {
                signupFlow.error = "Failed to load selected image"
                return
            }

            guard let uiImage = UIImage(data: data) else {
                signupFlow.error = "Invalid image format"
                return
            }

            let resizedImage = resizeImage(uiImage, maxSize: 1024)
            guard let compressedData = resizedImage.jpegData(compressionQuality: 0.8) else {
                signupFlow.error = "Failed to process image"
                return
            }

            signupFlow.profilePhotoImage = resizedImage
            signupFlow.profilePhotoData = compressedData
        } catch {
            signupFlow.error = "Failed to load image: \(error.localizedDescription)"
        }
    }

    @MainActor
    private func resizeImage(_ image: UIImage, maxSize: CGFloat) -> UIImage {
        let size = image.size
        guard size.width > maxSize || size.height > maxSize else { return image }
        let ratio = min(maxSize / size.width, maxSize / size.height)
        let newSize = CGSize(width: size.width * ratio, height: size.height * ratio)
        UIGraphicsBeginImageContextWithOptions(newSize, false, 1.0)
        image.draw(in: CGRect(origin: .zero, size: newSize))
        let newImage = UIGraphicsGetImageFromCurrentImageContext()
        UIGraphicsEndImageContext()
        return newImage ?? image
    }

    private func handleContinue() {
        if signupFlow.hasProfilePhoto {
            uploadPhotoAndContinue()
        } else {
            onSkip()
        }
    }

    private func uploadPhotoAndContinue() {
        guard let photoData = signupFlow.profilePhotoData else {
            onContinue()
            return
        }

        isUploading = true
        signupFlow.clearError()

        Task { @MainActor in
            defer { isUploading = false }
            do {
                try await authStore.uploadProfilePhoto(
                    fileData: photoData,
                    filename: "profile.jpg",
                    mimeType: "image/jpeg"
                )
                onContinue()
            } catch {
                signupFlow.error = "Failed to upload photo. Please try again."
            }
        }
    }
}

// MARK: - Resume Documents Step

struct ResumeDocumentsStepView: View {
    @EnvironmentObject private var signupFlow: SignupFlowState
    @EnvironmentObject private var authStore: AuthStore

    let onComplete: () -> Void

    @State private var isCompletingOnboarding = false

    var body: some View {
        SignupStepContainer(
            title: "Driver Verification",
            currentStep: signupFlow.currentStepIndex,
            totalSteps: signupFlow.totalSteps,
            showBackButton: false,
            isLoading: isCompletingOnboarding,
            canContinue: authStore.hasRequiredDocuments(),
            onBack: nil,
            onContinue: { completeOnboarding() }
        ) {
            VStack(spacing: 24) {
                SignupStepHeader(
                    title: "Upload Your Documents",
                    subtitle: "We need to verify your identity before you can start driving.",
                    icon: "doc.badge.plus"
                )

                EmbeddedDocumentUploadContent()

                VStack(alignment: .leading, spacing: 8) {
                    Label("Accepted formats: JPEG, PNG", systemImage: "info.circle")
                    Label("Maximum file size: 10MB", systemImage: "doc")
                    Label("Documents will be reviewed within 24 hours", systemImage: "clock")
                }
                .font(.caption)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal)

                if let error = signupFlow.error ?? authStore.error {
                    SignupErrorBanner(message: error)
                }
            }
        }
        .task {
            await authStore.fetchDocuments()
        }
    }

    private func completeOnboarding() {
        isCompletingOnboarding = true
        signupFlow.clearError()

        Task { @MainActor in
            defer { isCompletingOnboarding = false }
            do {
                try await authStore.completeOnboarding()
                onComplete()
            } catch {
                signupFlow.error = authStore.error ?? "Failed to complete onboarding"
            }
        }
    }
}

import PhotosUI

#Preview {
    ContentView()
        .environmentObject(AuthStore.shared)
        .environmentObject(DeepLinkRouter.shared)
}
