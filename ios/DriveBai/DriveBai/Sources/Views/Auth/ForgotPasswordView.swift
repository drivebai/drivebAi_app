import SwiftUI

// MARK: - Forgot Password View

struct ForgotPasswordView: View {
    @EnvironmentObject private var authStore: AuthStore
    @Environment(\.dismiss) private var dismiss

    @State private var email = ""
    @State private var showEmailSentModal = false

    private var isValidEmail: Bool {
        let emailRegex = #"^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$"#
        return email.range(of: emailRegex, options: .regularExpression) != nil
    }

    var body: some View {
        ZStack {
            VStack(spacing: 32) {
                // Header
                VStack(spacing: 16) {
                    Image(systemName: "lock.rotation")
                        .font(.system(size: 60))
                        .foregroundColor(.accentColor)

                    Text("Forgot password")
                        .font(.title2)
                        .fontWeight(.bold)

                    Text("We'll send an email with instructions to reset your password")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                }
                .padding(.top, 40)
                .padding(.horizontal)

                // Form
                VStack(spacing: 16) {
                    TextField("Email", text: $email)
                        .textFieldStyle(DriveBaiTextFieldStyle())
                        .textContentType(.emailAddress)
                        .autocapitalization(.none)
                        .keyboardType(.emailAddress)

                    if let error = authStore.error {
                        Text(error)
                            .font(.footnote)
                            .foregroundColor(.red)
                            .multilineTextAlignment(.center)
                    }
                }
                .padding(.horizontal)

                Spacer()

                // Continue Button
                Button(action: requestReset) {
                    if authStore.isLoading {
                        ProgressView()
                            .tint(.white)
                    } else {
                        Text("Continue")
                    }
                }
                .buttonStyle(DriveBaiButtonStyle())
                .disabled(!isValidEmail || authStore.isLoading)
                .padding(.horizontal)
                .padding(.bottom, 32)
            }
            .navigationBarTitleDisplayMode(.inline)

            // Modal Overlay
            if showEmailSentModal {
                CheckEmailModalView(
                    email: email,
                    onClose: {
                        showEmailSentModal = false
                        dismiss()
                    },
                    onResend: {
                        Task {
                            try? await authStore.forgotPassword(email: email)
                        }
                    }
                )
            }
        }
        .onDisappear {
            authStore.clearError()
        }
    }

    private func requestReset() {
        Task {
            do {
                try await authStore.forgotPassword(email: email)
                showEmailSentModal = true
            } catch {
                // Error is already set in authStore
            }
        }
    }
}

// MARK: - Check Email Modal View

struct CheckEmailModalView: View {
    let email: String
    let onClose: () -> Void
    let onResend: () -> Void

    @EnvironmentObject private var authStore: AuthStore

    var body: some View {
        ZStack {
            // Background overlay
            Color.black.opacity(0.5)
                .ignoresSafeArea()
                .onTapGesture {
                    // Dismiss on background tap
                    onClose()
                }

            // Modal content
            VStack(spacing: 24) {
                // Close button
                HStack {
                    Spacer()
                    Button(action: onClose) {
                        Image(systemName: "xmark")
                            .font(.system(size: 16, weight: .medium))
                            .foregroundColor(.secondary)
                            .padding(8)
                            .background(Color(.systemGray5))
                            .clipShape(Circle())
                    }
                }

                // Icon
                Image(systemName: "envelope.badge")
                    .font(.system(size: 60))
                    .foregroundColor(.accentColor)

                // Title
                Text("Check your email")
                    .font(.title2)
                    .fontWeight(.bold)

                // Message
                Text("We've sent you a reset link to your email. Please follow instructions in the email to reset your password.")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal)

                // Resend button
                Button(action: onResend) {
                    if authStore.isLoading {
                        ProgressView()
                            .tint(.accentColor)
                    } else {
                        Text("Resend email")
                            .foregroundColor(.accentColor)
                    }
                }
                .disabled(authStore.isLoading)
                .padding(.top, 8)
            }
            .padding(24)
            .background(Color(.systemBackground))
            .cornerRadius(20)
            .shadow(radius: 20)
            .padding(.horizontal, 32)
        }
        .transition(.opacity.combined(with: .scale(scale: 0.9)))
        .animation(.easeInOut(duration: 0.2), value: true)
    }
}

// MARK: - Reset Password View (Deep Link Entry Point)

struct ResetPasswordView: View {
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var deepLinkRouter: DeepLinkRouter
    @Environment(\.dismiss) private var dismiss

    let token: String

    @State private var password = ""
    @State private var confirmPassword = ""
    @State private var showPassword = false
    @State private var showConfirmPassword = false
    @State private var showSuccessModal = false

    private var isFormValid: Bool {
        password.count >= 8 && password == confirmPassword
    }

    var body: some View {
        ZStack {
            VStack(spacing: 32) {
                // Header
                VStack(spacing: 16) {
                    Text("Forgot password")
                        .font(.title2)
                        .fontWeight(.bold)

                    Text("Choose a new password")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                }
                .padding(.top, 40)

                // Form
                VStack(spacing: 16) {
                    // Password field with eye toggle
                    PasswordFieldWithToggle(
                        placeholder: "Password",
                        text: $password,
                        isSecure: !showPassword,
                        onToggle: { showPassword.toggle() }
                    )

                    // Confirm password field with eye toggle
                    PasswordFieldWithToggle(
                        placeholder: "Password (confirm)",
                        text: $confirmPassword,
                        isSecure: !showConfirmPassword,
                        onToggle: { showConfirmPassword.toggle() }
                    )

                    // Validation messages
                    VStack(alignment: .leading, spacing: 4) {
                        if !password.isEmpty && password.count < 8 {
                            Text("Password must be at least 8 characters")
                                .font(.caption)
                                .foregroundColor(.red)
                        }

                        if !confirmPassword.isEmpty && password != confirmPassword {
                            Text("Passwords do not match")
                                .font(.caption)
                                .foregroundColor(.red)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)

                    if let error = authStore.error {
                        Text(error)
                            .font(.footnote)
                            .foregroundColor(.red)
                            .multilineTextAlignment(.center)
                    }
                }
                .padding(.horizontal)

                Spacer()

                // Continue Button
                Button(action: resetPassword) {
                    if authStore.isLoading {
                        ProgressView()
                            .tint(.white)
                    } else {
                        Text("Continue")
                    }
                }
                .buttonStyle(DriveBaiButtonStyle())
                .disabled(!isFormValid || authStore.isLoading)
                .padding(.horizontal)
                .padding(.bottom, 32)
            }
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button(action: { handleDismiss() }) {
                        Image(systemName: "xmark")
                            .foregroundColor(.primary)
                    }
                }
            }

            // Success Modal Overlay
            if showSuccessModal {
                PasswordUpdatedModalView(
                    onLogin: {
                        showSuccessModal = false
                        handleDismiss()
                    }
                )
            }
        }
        .onDisappear {
            authStore.clearError()
        }
    }

    private func resetPassword() {
        Task {
            do {
                try await authStore.resetPassword(token: token, newPassword: password)
                showSuccessModal = true
            } catch {
                // Error is already set in authStore
            }
        }
    }

    private func handleDismiss() {
        deepLinkRouter.clearPendingRoute()
        dismiss()
    }
}

// MARK: - Password Field with Eye Toggle

struct PasswordFieldWithToggle: View {
    let placeholder: String
    @Binding var text: String
    let isSecure: Bool
    let onToggle: () -> Void

    var body: some View {
        HStack {
            if isSecure {
                SecureField(placeholder, text: $text)
                    .textContentType(.newPassword)
                    .autocapitalization(.none)
                    .autocorrectionDisabled()
            } else {
                TextField(placeholder, text: $text)
                    .textContentType(.newPassword)
                    .autocapitalization(.none)
                    .autocorrectionDisabled()
            }

            Button(action: onToggle) {
                Image(systemName: isSecure ? "eye.slash" : "eye")
                    .foregroundColor(.secondary)
            }
        }
        .padding()
        .background(Color(.systemGray6))
        .cornerRadius(12)
        .overlay(
            RoundedRectangle(cornerRadius: 12)
                .stroke(Color.gray.opacity(0.2), lineWidth: 1)
        )
    }
}

// MARK: - Password Updated Modal View

struct PasswordUpdatedModalView: View {
    let onLogin: () -> Void

    var body: some View {
        ZStack {
            // Background overlay
            Color.black.opacity(0.5)
                .ignoresSafeArea()

            // Modal content
            VStack(spacing: 24) {
                // Icon
                Image(systemName: "checkmark.circle.fill")
                    .font(.system(size: 60))
                    .foregroundColor(.green)

                // Title
                Text("Password updated")
                    .font(.title2)
                    .fontWeight(.bold)

                // Message
                Text("Log in using your email and your new password")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal)

                // Log in button
                Button(action: onLogin) {
                    Text("Log in")
                        .fontWeight(.semibold)
                        .foregroundColor(.white)
                        .frame(maxWidth: .infinity)
                        .padding()
                        .background(Color.accentColor)
                        .cornerRadius(12)
                }
                .padding(.top, 8)
            }
            .padding(24)
            .background(Color(.systemBackground))
            .cornerRadius(20)
            .shadow(radius: 20)
            .padding(.horizontal, 32)
        }
        .transition(.opacity.combined(with: .scale(scale: 0.9)))
        .animation(.easeInOut(duration: 0.2), value: true)
    }
}

// MARK: - Preview

#Preview {
    NavigationStack {
        ForgotPasswordView()
            .environmentObject(AuthStore.shared)
    }
}
