import SwiftUI

// MARK: - Keyboard Dismiss Helper

private extension View {
    func dismissKeyboard() {
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
    }
}

// MARK: - Enter Email OTP View

/// Primary authentication entry point. User enters their email, then gets a 6-digit
/// code to verify. Existing users are logged in; new users are routed to sign-up.
struct EnterEmailOTPView: View {
    @EnvironmentObject private var authStore: AuthStore
    @Environment(\.dismiss) private var dismiss

    let showDismissButton: Bool
    /// Guest-mode conversion context raised by a gated CTA: the specific ask
    /// (title) and its persuasive body replace the generic welcome header.
    /// Both nil → the default "Welcome to DrivaBai" header (Sign-in tab / root).
    let contextTitle: String?
    let contextBody: String?
    /// First-time guest treatment on the Sign-in tab: a short 3-point value
    /// summary plus a "How DriveBai works" entry point above the email field.
    let showGuestIntro: Bool

    init(
        showDismissButton: Bool = true,
        contextTitle: String? = nil,
        contextBody: String? = nil,
        showGuestIntro: Bool = false
    ) {
        self.showDismissButton = showDismissButton
        self.contextTitle = contextTitle
        self.contextBody = contextBody
        self.showGuestIntro = showGuestIntro
    }

    @State private var email = ""
    @State private var showCodeEntry = false
    @State private var showPasswordLogin = false
    @State private var showHowItWorks = false
    @FocusState private var emailFocused: Bool

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 0) {
                    // ── Brand header ───────────────────────────────────────
                    VStack(spacing: 14) {
                        ZStack {
                            RoundedRectangle(cornerRadius: 20)
                                .fill(Color.driveBaiPrimary.opacity(0.12))
                                .frame(width: 80, height: 80)
                            Image(systemName: "car.fill")
                                .font(.system(size: 34, weight: .semibold))
                                .foregroundColor(.driveBaiPrimary)
                        }
                        .padding(.top, 48)

                        VStack(spacing: 6) {
                            Text(contextTitle ?? "Welcome to DrivaBai")
                                .font(.title2)
                                .fontWeight(.bold)
                                .multilineTextAlignment(.center)
                                .padding(.horizontal, 24)
                            Text(contextBody ?? "Enter your email to sign in or create an account")
                                .font(.subheadline)
                                .foregroundColor(contextBody == nil ? .secondary : .primary)
                                .multilineTextAlignment(.center)
                                .padding(.horizontal, 24)
                        }
                    }
                    .padding(.bottom, showGuestIntro ? 20 : 36)

                    // ── First-time guest intro (Sign-in tab) ───────────────
                    if showGuestIntro {
                        guestIntroBlock
                            .padding(.horizontal, 24)
                            .padding(.bottom, 28)
                    }

                    // ── Email input ────────────────────────────────────────
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Email address")
                            .font(.footnote)
                            .fontWeight(.medium)
                            .foregroundColor(.secondary)
                            .padding(.leading, 2)

                        TextField("you@example.com", text: $email)
                            .textFieldStyle(DriveBaiTextFieldStyle())
                            .textContentType(.emailAddress)
                            .autocapitalization(.none)
                            .keyboardType(.emailAddress)
                            .autocorrectionDisabled()
                            .focused($emailFocused)
                            .submitLabel(.continue)
                            .onSubmit { requestCode() }
                    }
                    .padding(.horizontal, 24)
                    .padding(.bottom, 16)

                    // ── Error ──────────────────────────────────────────────
                    if let err = authStore.error {
                        OTPErrorBanner(message: err)
                            .padding(.horizontal, 24)
                            .padding(.bottom, 16)
                    }

                    // ── Primary CTA ────────────────────────────────────────
                    Button(action: requestCode) {
                        if authStore.isLoading {
                            ProgressView().tint(.white)
                        } else {
                            Text("Continue")
                        }
                    }
                    .buttonStyle(DriveBaiButtonStyle())
                    .disabled(authStore.isLoading || !isEmailValid)
                    .padding(.horizontal, 24)
                    .padding(.bottom, 24)

                    // ── Divider ────────────────────────────────────────────
                    HStack(spacing: 12) {
                        Rectangle().fill(Color.gray.opacity(0.2)).frame(height: 1)
                        Text("or")
                            .font(.footnote)
                            .foregroundColor(.secondary)
                        Rectangle().fill(Color.gray.opacity(0.2)).frame(height: 1)
                    }
                    .padding(.horizontal, 24)
                    .padding(.bottom, 20)

                    // ── Secondary: password login ──────────────────────────
                    Button(action: { showPasswordLogin = true }) {
                        Text("Use password instead")
                            .font(.subheadline)
                            .fontWeight(.medium)
                            .foregroundColor(.driveBaiPrimary)
                    }
                    .padding(.bottom, 48)
                }
            }
            .scrollDismissesKeyboard(.interactively)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if showDismissButton {
                    ToolbarItem(placement: .navigationBarLeading) {
                        Button(action: { dismiss() }) {
                            Image(systemName: "xmark")
                                .font(.system(size: 14, weight: .semibold))
                                .foregroundColor(.primary)
                        }
                    }
                }
            }
            .navigationDestination(isPresented: $showCodeEntry) {
                OTPCodeInputView(email: email.lowercased().trimmingCharacters(in: .whitespacesAndNewlines))
            }
            .fullScreenCover(isPresented: $showPasswordLogin) {
                LoginView(showDismissButton: true)
                    .environmentObject(authStore)
            }
            .sheet(isPresented: $showHowItWorks) {
                // "Sign up to list your car" from here: the guest is already
                // on the sign-in screen, so we just pre-select the owner role
                // (consumed at the signup role step) and close — the email
                // field is right below. No second prompt.
                HowDriveBaiWorksView(onSignUpAsOwner: {
                    DeepLinkRouter.shared.guestSignupRoleHint = .carOwner
                    showHowItWorks = false
                })
            }
            .onAppear {
                authStore.clearError()
                // Slight delay so the keyboard appears after the view settles
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.45) {
                    emailFocused = true
                }
            }
        }
    }

    // First-time guest intro shown on the Sign-in tab above the email field.
    private var guestIntroBlock: some View {
        VStack(alignment: .leading, spacing: 14) {
            introPoint("magnifyingglass", "Browse every car free — no account needed.")
            introPoint("envelope", "Sign up with just your email.")
            introPoint("checkmark.shield", "Add a license or payment only when you rent or buy.")

            Button(action: { showHowItWorks = true }) {
                HStack(spacing: 4) {
                    Text("How DriveBai works")
                    Image(systemName: "chevron.right").font(.caption2)
                }
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.driveBaiPrimary)
            }
            .padding(.top, 2)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .background(Color(.systemGray6))
        .cornerRadius(14)
    }

    private func introPoint(_ icon: String, _ text: String) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: icon)
                .font(.system(size: 14, weight: .semibold))
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 20)
            Text(text)
                .font(.subheadline)
                .foregroundColor(.primary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private var isEmailValid: Bool {
        let t = email.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.contains("@") && t.count > 4
    }

    private func requestCode() {
        guard isEmailValid, !authStore.isLoading else { return }
        let trimmed = email.lowercased().trimmingCharacters(in: .whitespacesAndNewlines)
        // Mark that this device has started sign-in before, so the guest
        // Sign-in tab greets a returning visitor differently from a
        // first-timer. Set on submit regardless of whether it completes.
        GuestOnboardingStore.shared.markEnteredEmail()
        Task {
            do {
                try await authStore.requestLoginOTP(email: trimmed)
                showCodeEntry = true
                #if DEBUG
                print("[OTP] Code requested for: \(trimmed)")
                #endif
            } catch {
                // authStore.error displayed inline
            }
        }
    }
}

// MARK: - OTP Code Input View

/// Displays 6 individual digit boxes and handles code verification.
struct OTPCodeInputView: View {
    @EnvironmentObject private var authStore: AuthStore
    @Environment(\.dismiss) private var dismiss
    @FocusState private var isInputFocused: Bool

    let email: String

    @State private var code = ""
    @State private var showSignupFlow = false
    @State private var resendCountdown = 60
    @State private var canResend = false
    private let timer = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    var body: some View {
        ScrollView {
            VStack(spacing: 28) {
                // ── Header ─────────────────────────────────────────────────
                VStack(spacing: 12) {
                    Image(systemName: "envelope.open.fill")
                        .font(.system(size: 44, weight: .regular))
                        .foregroundColor(.driveBaiPrimary)
                        .padding(.top, 40)

                    Text("Check your email")
                        .font(.title2)
                        .fontWeight(.bold)

                    VStack(spacing: 3) {
                        Text("We sent a 6-digit code to")
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                        Text(email)
                            .font(.subheadline)
                            .fontWeight(.semibold)
                    }
                }

                // ── 6-box code input ───────────────────────────────────────
                OTPBoxesInput(code: $code, isFocused: $isInputFocused)
                    .onTapGesture { isInputFocused = true }

                // ── Error ──────────────────────────────────────────────────
                if let err = authStore.error {
                    OTPErrorBanner(message: err)
                        .padding(.horizontal, 24)
                }

                // ── Verify CTA ─────────────────────────────────────────────
                Button(action: verify) {
                    if authStore.isLoading {
                        ProgressView().tint(.white)
                    } else {
                        Text("Verify Code")
                    }
                }
                .buttonStyle(DriveBaiButtonStyle())
                .disabled(authStore.isLoading || code.count != 6)
                .padding(.horizontal, 24)

                // ── Resend ─────────────────────────────────────────────────
                Group {
                    if canResend {
                        Button(action: resendCode) {
                            Label("Resend code", systemImage: "arrow.clockwise")
                                .font(.subheadline)
                                .fontWeight(.medium)
                                .foregroundColor(.driveBaiPrimary)
                        }
                    } else {
                        Text("Resend available in \(resendCountdown)s")
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                    }
                }
                .padding(.bottom, 32)
            }
        }
        .scrollDismissesKeyboard(.interactively)
        .navigationBarTitleDisplayMode(.inline)
        .navigationBarBackButtonHidden(true)
        .toolbar {
            ToolbarItem(placement: .navigationBarLeading) {
                Button(action: { dismiss() }) {
                    HStack(spacing: 4) {
                        Image(systemName: "chevron.left")
                            .font(.system(size: 14, weight: .semibold))
                        Text("Change email")
                            .font(.subheadline)
                            .fontWeight(.medium)
                    }
                    .foregroundColor(.driveBaiPrimary)
                }
            }
        }
        .onReceive(timer) { _ in
            if resendCountdown > 0 { resendCountdown -= 1 } else { canResend = true }
        }
        .onAppear {
            authStore.clearError()
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
                isInputFocused = true
            }
        }
        .onDisappear {
            isInputFocused = false
            dismissKeyboard()
        }
        .onChange(of: code) { _, newValue in
            // Clamp to 6 numeric digits
            let filtered = String(newValue.filter { $0.isNumber }.prefix(6))
            if filtered != code {
                code = filtered
                return // next onChange will re-evaluate
            }
            // Auto-verify when all 6 digits entered
            if filtered.count == 6, !authStore.isLoading {
                verify()
            }
        }
        .fullScreenCover(isPresented: $showSignupFlow) {
            if let regData = authStore.pendingRegistrationTokenData {
                SignupFlowView(mode: .otp(registrationToken: regData.registrationToken, email: regData.email))
                    .environmentObject(authStore)
            }
        }
    }

    private func verify() {
        isInputFocused = false
        dismissKeyboard()
        Task {
            do {
                let isLogin = try await authStore.verifyLoginOTP(email: email, code: code)
                if !isLogin {
                    showSignupFlow = true
                }
                #if DEBUG
                print("[OTP] Verify result: \(isLogin ? "login" : "register")")
                #endif
            } catch {
                code = ""
                isInputFocused = true
            }
        }
    }

    private func resendCode() {
        guard !authStore.isLoading else { return }
        canResend = false
        resendCountdown = 60
        code = ""
        isInputFocused = true
        Task {
            do {
                try await authStore.requestLoginOTP(email: email)
                #if DEBUG
                print("[OTP] Code resent for: \(email)")
                #endif
            } catch {
                // authStore.error shown inline
            }
        }
    }
}

// MARK: - OTP Boxes Input

/// Six individual digit boxes backed by a hidden text field for capture.
struct OTPBoxesInput: View {
    @Binding var code: String
    @FocusState.Binding var isFocused: Bool

    var body: some View {
        ZStack {
            // Hidden field that captures keyboard input
            TextField("", text: $code)
                .keyboardType(.numberPad)
                .textContentType(.oneTimeCode)
                .focused($isFocused)
                .frame(width: 1, height: 1)
                .opacity(0.001)
                .allowsHitTesting(false)

            // Visual digit boxes
            HStack(spacing: 10) {
                ForEach(0..<6, id: \.self) { index in
                    OTPDigitBox(
                        character: character(at: index),
                        isFilled: index < code.count,
                        isActive: code.count == index && isFocused
                    )
                }
            }
        }
    }

    private func character(at index: Int) -> String? {
        guard index < code.count else { return nil }
        return String(code[code.index(code.startIndex, offsetBy: index)])
    }
}

// MARK: - OTP Digit Box

struct OTPDigitBox: View {
    let character: String?
    let isFilled: Bool
    let isActive: Bool

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 12)
                .fill(Color(.systemGray6))
                .frame(width: 48, height: 58)
                .overlay(
                    RoundedRectangle(cornerRadius: 12)
                        .strokeBorder(borderColor, lineWidth: isActive ? 2 : 1)
                )

            if let char = character {
                Text(char)
                    .font(.system(size: 22, weight: .bold, design: .monospaced))
                    .foregroundColor(.primary)
                    .transition(.scale.combined(with: .opacity))
            } else if isActive {
                // Cursor bar
                Capsule()
                    .fill(Color.driveBaiPrimary)
                    .frame(width: 2, height: 24)
            }
        }
        .animation(.easeInOut(duration: 0.1), value: isFilled)
        .animation(.easeInOut(duration: 0.15), value: isActive)
    }

    private var borderColor: Color {
        if isActive { return .driveBaiPrimary }
        if isFilled { return Color.driveBaiPrimary.opacity(0.35) }
        return Color.gray.opacity(0.25)
    }
}

// MARK: - OTP Error Banner

struct OTPErrorBanner: View {
    let message: String

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: "exclamationmark.circle.fill")
                .font(.subheadline)
                .foregroundColor(.red)
            Text(message)
                .font(.footnote)
                .foregroundColor(.red)
                .multilineTextAlignment(.leading)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .background(Color.red.opacity(0.08))
        .cornerRadius(10)
    }
}

#Preview {
    EnterEmailOTPView(showDismissButton: false)
        .environmentObject(AuthStore.shared)
}
