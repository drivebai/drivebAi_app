import SwiftUI

// MARK: - Signup Progress Bar (Segmented Lines)

/// Progress bar with segmented lines - appears at bottom above Continue button
struct SignupProgressBar: View {
    let current: Int  // 0-indexed current step
    let total: Int

    var body: some View {
        HStack(spacing: 8) {
            ForEach(0..<total, id: \.self) { index in
                RoundedRectangle(cornerRadius: 2)
                    .fill(index <= current ? Color.driveBaiPrimary : Color.gray.opacity(0.3))
                    .frame(height: 4)
            }
        }
        .animation(.easeInOut(duration: 0.2), value: current)
        .animation(.easeInOut(duration: 0.2), value: total)
    }
}

// MARK: - Signup Wizard Container

/// A wizard container with:
/// - Scrollable content area
/// - Sticky footer with progress bar + Continue button
struct SignupWizardContainer<Content: View>: View {
    let currentStep: Int
    let totalSteps: Int
    let isLoading: Bool
    let canContinue: Bool
    let continueTitle: String
    let showSkip: Bool
    let skipTitle: String
    let onContinue: () -> Void
    let onSkip: (() -> Void)?
    let content: Content

    init(
        currentStep: Int,
        totalSteps: Int,
        isLoading: Bool = false,
        canContinue: Bool = true,
        continueTitle: String = "Continue",
        showSkip: Bool = false,
        skipTitle: String = "Skip",
        onContinue: @escaping () -> Void,
        onSkip: (() -> Void)? = nil,
        @ViewBuilder content: () -> Content
    ) {
        self.currentStep = currentStep
        self.totalSteps = totalSteps
        self.isLoading = isLoading
        self.canContinue = canContinue
        self.continueTitle = continueTitle
        self.showSkip = showSkip
        self.skipTitle = skipTitle
        self.onContinue = onContinue
        self.onSkip = onSkip
        self.content = content()
    }

    var body: some View {
        VStack(spacing: 0) {
            // Scrollable content
            ScrollView {
                content
                    .padding(.bottom, 24) // Space before footer
            }
            .scrollDismissesKeyboard(.interactively)

            // Sticky footer with progress + CTA
            VStack(spacing: 16) {
                Divider()

                // Progress bar
                SignupProgressBar(current: currentStep, total: totalSteps)
                    .padding(.horizontal)
                    .padding(.top, 8)

                // Continue button
                SignupCTAButton(
                    title: continueTitle,
                    isLoading: isLoading,
                    isEnabled: canContinue && !isLoading,
                    action: onContinue
                )
                .padding(.horizontal)

                // Optional secondary button (Skip/Reupload)
                if showSkip, let onSkip = onSkip {
                    Button(action: onSkip) {
                        Text(skipTitle)
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                    }
                    .disabled(isLoading)
                }
            }
            .padding(.bottom, 32)
            .background(Color(.systemBackground))
        }
        .ignoresSafeArea(.keyboard, edges: .bottom)
    }
}

// MARK: - Signup Step Container

/// Container for individual signup steps with back button support
struct SignupStepContainer<Content: View>: View {
    let title: String
    let currentStep: Int
    let totalSteps: Int
    let showBackButton: Bool
    let isLoading: Bool
    let canContinue: Bool
    let continueTitle: String
    let showSkip: Bool
    let skipTitle: String
    let onBack: (() -> Void)?
    let onContinue: () -> Void
    let onSkip: (() -> Void)?
    let content: Content

    init(
        title: String,
        currentStep: Int,
        totalSteps: Int,
        showBackButton: Bool = true,
        isLoading: Bool = false,
        canContinue: Bool = true,
        continueTitle: String = "Continue",
        showSkip: Bool = false,
        skipTitle: String = "Skip",
        onBack: (() -> Void)? = nil,
        onContinue: @escaping () -> Void,
        onSkip: (() -> Void)? = nil,
        @ViewBuilder content: () -> Content
    ) {
        self.title = title
        self.currentStep = currentStep
        self.totalSteps = totalSteps
        self.showBackButton = showBackButton
        self.isLoading = isLoading
        self.canContinue = canContinue
        self.continueTitle = continueTitle
        self.showSkip = showSkip
        self.skipTitle = skipTitle
        self.onBack = onBack
        self.onContinue = onContinue
        self.onSkip = onSkip
        self.content = content()
    }

    var body: some View {
        SignupWizardContainer(
            currentStep: currentStep,
            totalSteps: totalSteps,
            isLoading: isLoading,
            canContinue: canContinue,
            continueTitle: continueTitle,
            showSkip: showSkip,
            skipTitle: skipTitle,
            onContinue: onContinue,
            onSkip: onSkip
        ) {
            content
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
        .navigationBarBackButtonHidden(true)
        .toolbar {
            ToolbarItem(placement: .navigationBarLeading) {
                if showBackButton, let onBack = onBack {
                    // Visible "Back" affordance — icon alone was too easy to
                    // miss in QA. Includes a tappable text label so users
                    // know they can return to the previous step (or, on
                    // step 1, dismiss the signup sheet back to login).
                    Button(action: onBack) {
                        HStack(spacing: 4) {
                            Image(systemName: "chevron.left")
                            Text("Back")
                        }
                        .font(.body)
                        .foregroundColor(.driveBaiPrimary)
                    }
                    .disabled(isLoading)
                    .accessibilityLabel("Back")
                }
            }
        }
    }
}

// MARK: - Primary CTA Button

struct SignupCTAButton: View {
    let title: String
    let isLoading: Bool
    let isEnabled: Bool
    let action: () -> Void

    init(
        title: String = "Continue",
        isLoading: Bool = false,
        isEnabled: Bool = true,
        action: @escaping () -> Void
    ) {
        self.title = title
        self.isLoading = isLoading
        self.isEnabled = isEnabled
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            HStack {
                if isLoading {
                    ProgressView()
                        .tint(.white)
                } else {
                    Text(title)
                        .fontWeight(.semibold)
                }
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 16)
            .background(
                RoundedRectangle(cornerRadius: 12)
                    .fill(isEnabled ? Color.driveBaiPrimary : Color.gray.opacity(0.3))
            )
            .foregroundColor(.white)
        }
        .disabled(!isEnabled || isLoading)
    }
}

// MARK: - Step Header

struct SignupStepHeader: View {
    let title: String
    let subtitle: String?
    let icon: String?

    init(title: String, subtitle: String? = nil, icon: String? = nil) {
        self.title = title
        self.subtitle = subtitle
        self.icon = icon
    }

    var body: some View {
        VStack(spacing: 12) {
            if let icon = icon {
                Image(systemName: icon)
                    .font(.system(size: 48))
                    .foregroundColor(.driveBaiPrimary)
                    .padding(.bottom, 8)
            }

            Text(title)
                .font(.title2)
                .fontWeight(.bold)
                .multilineTextAlignment(.center)

            if let subtitle = subtitle {
                Text(subtitle)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 24)
            }
        }
        .padding(.top, 24)
        .padding(.bottom, 16)
    }
}

// MARK: - Error Banner

struct SignupErrorBanner: View {
    let message: String

    var body: some View {
        Text(message)
            .font(.footnote)
            .foregroundColor(.red)
            .multilineTextAlignment(.center)
            .padding(.horizontal)
            .padding(.vertical, 8)
    }
}

// MARK: - Previews

#Preview("Progress Bar") {
    VStack(spacing: 32) {
        SignupProgressBar(current: 0, total: 3)
        SignupProgressBar(current: 1, total: 3)
        SignupProgressBar(current: 2, total: 3)

        Divider()

        SignupProgressBar(current: 0, total: 4)
        SignupProgressBar(current: 1, total: 4)
        SignupProgressBar(current: 2, total: 4)
        SignupProgressBar(current: 3, total: 4)
    }
    .padding()
}

#Preview("CTA Button") {
    VStack(spacing: 16) {
        SignupCTAButton(isEnabled: true) { }
        SignupCTAButton(isEnabled: false) { }
        SignupCTAButton(isLoading: true) { }
    }
    .padding()
}

#Preview("Wizard Container") {
    NavigationStack {
        SignupStepContainer(
            title: "Finish signing up",
            currentStep: 0,
            totalSteps: 3,
            showBackButton: false,
            canContinue: true,
            onBack: nil,
            onContinue: {}
        ) {
            VStack(spacing: 16) {
                SignupStepHeader(
                    title: "Create your account",
                    subtitle: "Enter your details to get started"
                )

                Text("Form content goes here")
                    .padding()
            }
        }
    }
}
