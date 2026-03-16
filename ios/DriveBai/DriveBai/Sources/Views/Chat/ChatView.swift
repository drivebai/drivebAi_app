import SwiftUI
import StripePaymentSheet

struct ChatView: View {
    let chatId: UUID
    let currentUserId: UUID
    let counterpartyId: UUID
    let counterpartyName: String

    @StateObject private var viewModel: ChatViewModel
    @EnvironmentObject private var authStore: AuthStore
    @State private var showRequestComposer = false
    @State private var showDetails = false
    @State private var showProfile = false
    @State private var showPaymentSheet = false
    @State private var paymentIntentResponse: PaymentIntentAPIResponse?
    @State private var paymentLeaseRequestId: UUID?

    init(chatId: UUID, currentUserId: UUID, counterpartyId: UUID, counterpartyName: String) {
        self.chatId = chatId
        self.currentUserId = currentUserId
        self.counterpartyId = counterpartyId
        self.counterpartyName = counterpartyName
        _viewModel = StateObject(wrappedValue: ChatViewModel(chatId: chatId, currentUserId: currentUserId))
    }

    var body: some View {
        VStack(spacing: 0) {
            // Tab picker
            Picker("", selection: $viewModel.selectedTab) {
                ForEach(ChatTab.allCases, id: \.self) { tab in
                    Text(tab.rawValue).tag(tab)
                }
            }
            .pickerStyle(.segmented)
            .padding(.horizontal)
            .padding(.vertical, 8)

            // Content
            switch viewModel.selectedTab {
            case .messages:
                messagesContent
            case .requests:
                requestsContent
            }
        }
        .navigationTitle(counterpartyName)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .navigationBarTrailing) {
                Menu {
                    Button { showProfile = true } label: {
                        Label("View Profile", systemImage: "person.circle")
                    }
                    Button { showDetails = true } label: {
                        Label("Chat Details", systemImage: "info.circle")
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
            }
        }
        .sheet(isPresented: $showRequestComposer) {
            if let user = authStore.state.user {
                RequestComposerSheet(chatId: chatId, userRole: user.role) {
                    await viewModel.loadRequests()
                }
            }
        }
        .navigationDestination(isPresented: $showDetails) {
            ChatDetailsView(chatId: chatId)
        }
        .navigationDestination(isPresented: $showProfile) {
            CounterpartyProfileView(userId: counterpartyId)
        }
        .task {
            await viewModel.loadInitialMessages()
            async let reqTask: () = viewModel.loadRequests()
            async let leaseTask: () = viewModel.loadLeaseRequests()
            _ = await (reqTask, leaseTask)
            await viewModel.markAsRead()
        }
        .alert("Error", isPresented: .constant(viewModel.error != nil)) {
            Button("OK") { viewModel.error = nil }
        } message: {
            if let error = viewModel.error {
                Text(error)
            }
        }
        .background {
            // Zero-size overlay that presents Stripe PaymentSheet natively (no double-modal)
            if showPaymentSheet,
               let response = paymentIntentResponse,
               let customerId = response.customerId,
               let ephemeralKey = response.ephemeralKeySecret {
                PaymentSheetPresenter(
                    clientSecret: response.paymentIntentClientSecret,
                    ephemeralKeySecret: ephemeralKey,
                    customerId: customerId,
                    publishableKey: response.publishableKey
                ) { result in
                    showPaymentSheet = false
                    handlePaymentResult(result)
                }
                .frame(width: 0, height: 0)
            }
        }
    }

    // MARK: - Messages Tab

    private var messagesContent: some View {
        VStack(spacing: 0) {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(spacing: 2) {
                        if viewModel.hasMoreMessages {
                            ProgressView()
                                .padding()
                                .onAppear {
                                    Task { await viewModel.loadMoreMessages() }
                                }
                        }

                        ForEach(viewModel.messages) { message in
                            ChatBubbleView(message: message)
                                .id(message.id)
                        }
                    }
                    .padding(.horizontal)
                }
                .defaultScrollAnchor(.bottom)
            }

            // Input bar
            ChatInputBar(
                text: $viewModel.messageText,
                onSend: { Task { await viewModel.sendMessage() } },
                onRequestTap: { showRequestComposer = true }
            )
        }
    }

    // MARK: - Requests Tab

    private var hasAnyRequests: Bool {
        !viewModel.requests.isEmpty || !viewModel.leaseRequests.isEmpty
    }

    private var isLoadingAnyRequests: Bool {
        viewModel.isLoadingRequests || viewModel.isLoadingLeaseRequests
    }

    private var requestsContent: some View {
        Group {
            if isLoadingAnyRequests && !hasAnyRequests {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if !hasAnyRequests {
                VStack(spacing: 16) {
                    Spacer()
                    Image(systemName: "doc.text")
                        .font(.system(size: 40))
                        .foregroundColor(.gray.opacity(0.5))
                    Text("No requests yet")
                        .font(.headline)
                    Text("Create a request using the + button in Messages")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                    Spacer()
                }
                .padding()
            } else {
                ScrollView {
                    LazyVStack(spacing: 12) {
                        // Lease requests first
                        ForEach(viewModel.leaseRequests) { leaseReq in
                            LeaseRequestCardView(
                                leaseRequest: leaseReq,
                                currentUserId: currentUserId,
                                onAccept: {
                                    Task { await viewModel.acceptLeaseRequest(id: leaseReq.id) }
                                },
                                onDecline: {
                                    Task { await viewModel.declineLeaseRequest(id: leaseReq.id) }
                                },
                                onPay: {
                                    Task { await handlePayment(for: leaseReq) }
                                },
                                onCancel: {
                                    Task { await viewModel.cancelLeaseRequest(id: leaseReq.id) }
                                }
                            )
                        }

                        // Regular chat requests
                        ForEach(viewModel.requests) { request in
                            RequestCardView(
                                request: request,
                                currentUserId: currentUserId,
                                onAccept: {
                                    Task { await viewModel.respondToRequest(requestId: request.id, action: .accept) }
                                },
                                onDecline: {
                                    Task { await viewModel.respondToRequest(requestId: request.id, action: .decline) }
                                }
                            )
                        }
                    }
                    .padding()
                }
            }
        }
    }

    private func handlePayment(for leaseRequest: LeaseRequest) async {
        guard let response = await viewModel.createPaymentIntent(leaseRequestId: leaseRequest.id) else { return }
        paymentIntentResponse = response
        paymentLeaseRequestId = leaseRequest.id
        showPaymentSheet = true
    }

    private func handlePaymentResult(_ result: PaymentSheetResult) {
        let leaseId = paymentLeaseRequestId
        paymentIntentResponse = nil
        paymentLeaseRequestId = nil

        switch result {
        case .completed:
            // Sync with Stripe (bypasses webhook) + poll for status update
            if let leaseId = leaseId {
                Task { await viewModel.handlePaymentCompleted(leaseRequestId: leaseId) }
            }
        case .canceled:
            // Refresh to show current state (payment may have been partially created)
            Task { await viewModel.loadLeaseRequests() }
        case .failed(let error):
            viewModel.error = "Payment failed: \(error.localizedDescription)"
            Task { await viewModel.loadLeaseRequests() }
        }
    }
}
