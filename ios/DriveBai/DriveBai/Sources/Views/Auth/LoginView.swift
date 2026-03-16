import SwiftUI

struct LoginView: View {
    @EnvironmentObject private var authStore: AuthStore
    @Environment(\.dismiss) private var dismiss

    @State private var email = ""
    @State private var password = ""
    @State private var showSignUp = false
    @State private var showForgotPassword = false

    let showDismissButton: Bool

    init(showDismissButton: Bool = false) {
        self.showDismissButton = showDismissButton
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 32) {
                    // Logo and Title
                    VStack(spacing: 16) {
                        RoundedRectangle(cornerRadius: 12)
                            .fill(Color.gray.opacity(0.3))
                            .frame(width: 80, height: 80)

                        VStack(spacing: 4) {
                            Text("Search. Connect.")
                                .font(.title)
                                .fontWeight(.bold)

                            Text("Drive")
                                .font(.title)
                                .fontWeight(.bold)
                                .foregroundColor(.accentColor)
                        }
                    }
                    .padding(.top, 40)

                    // Form
                    VStack(spacing: 16) {
                        TextField("Email", text: $email)
                            .textFieldStyle(DriveBaiTextFieldStyle())
                            .textContentType(.emailAddress)
                            .autocapitalization(.none)
                            .keyboardType(.emailAddress)

                        SecureField("Password", text: $password)
                            .textFieldStyle(DriveBaiTextFieldStyle())
                            .textContentType(.password)

                        HStack {
                            Spacer()
                            Button("Forgot your password?") {
                                showForgotPassword = true
                            }
                            .font(.footnote)
                            .foregroundColor(.secondary)
                        }
                    }
                    .padding(.horizontal)

                    // Error Message
                    if let error = authStore.error {
                        Text(error)
                            .font(.footnote)
                            .foregroundColor(.red)
                            .multilineTextAlignment(.center)
                            .padding(.horizontal)
                    }

                    // Login Button
                    VStack(spacing: 16) {
                        Button(action: login) {
                            if authStore.isLoading {
                                ProgressView()
                                    .tint(.white)
                            } else {
                                Text("Continue")
                            }
                        }
                        .buttonStyle(DriveBaiButtonStyle())
                        .disabled(authStore.isLoading || !isFormValid)
                        .padding(.horizontal)

                        Text("or")
                            .foregroundColor(.secondary)

                        // Social Login Buttons (placeholders)
                        VStack(spacing: 12) {
                            SocialLoginButton(provider: .google, action: {})
                            SocialLoginButton(provider: .facebook, action: {})
                        }
                        .padding(.horizontal)
                    }

                    // Sign Up Link
                    HStack {
                        Text("Don't have an account?")
                            .foregroundColor(.secondary)
                        Button("Sign up") {
                            showSignUp = true
                        }
                        .fontWeight(.semibold)
                    }
                    .font(.footnote)
                    .padding(.top, 8)
                }
                .padding(.bottom, 32)
            }
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if showDismissButton {
                    ToolbarItem(placement: .navigationBarLeading) {
                        Button(action: { dismiss() }) {
                            Image(systemName: "xmark")
                                .foregroundColor(.primary)
                        }
                    }
                }
            }
            .fullScreenCover(isPresented: $showSignUp) {
                SignupFlowView()
                    .environmentObject(authStore)
            }
            .navigationDestination(isPresented: $showForgotPassword) {
                ForgotPasswordView()
            }
        }
    }

    private var isFormValid: Bool {
        !email.isEmpty && !password.isEmpty
    }

    private func login() {
        Task {
            do {
                try await authStore.login(email: email, password: password)
            } catch {
                // Error is already set in authStore
            }
        }
    }
}

// MARK: - Social Login Button

enum SocialLoginProvider {
    case google
    case facebook

    var title: String {
        switch self {
        case .google: return "Continue with Google"
        case .facebook: return "Continue with Facebook"
        }
    }

    var icon: String {
        switch self {
        case .google: return "g.circle.fill"
        case .facebook: return "f.circle.fill"
        }
    }

    var color: Color {
        switch self {
        case .google: return .red
        case .facebook: return .blue
        }
    }
}

struct SocialLoginButton: View {
    let provider: SocialLoginProvider
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack {
                Image(systemName: provider.icon)
                    .foregroundColor(provider.color)
                Text(provider.title)
                    .foregroundColor(.primary)
            }
            .frame(maxWidth: .infinity)
            .padding()
            .background(Color(.systemBackground))
            .cornerRadius(12)
            .overlay(
                RoundedRectangle(cornerRadius: 12)
                    .stroke(Color.gray.opacity(0.3), lineWidth: 1)
            )
        }
        .disabled(true) // Placeholder - not implemented
        .opacity(0.6)
    }
}

#Preview {
    LoginView()
        .environmentObject(AuthStore.shared)
}
