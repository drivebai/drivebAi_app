# DrivaBai Chat + Requests System — Architecture Diagrams

---

## 1. High-Level Architecture

```mermaid
graph TB
    subgraph iOS["iOS App (SwiftUI)"]
        direction TB
        UI["Views<br/>ChatsListView · ChatView<br/>RequestComposerSheet · ChatDetailsView"]
        VM["ViewModels<br/>ChatsListViewModel · ChatViewModel<br/>RequestComposerVM · ChatDetailsVM"]
        WSM["WebSocketManager<br/>URLSessionWebSocketTask<br/>Reconnect + Polling Fallback"]
        API["APIClient<br/>URLSession · JWT Bearer"]

        UI --> VM
        VM --> API
        VM <-.->|Combine<br/>PassthroughSubject| WSM
    end

    subgraph Backend["Backend (Go · chi · pgx)"]
        direction TB
        MW["Auth Middleware<br/>JWT validation"]
        CH["ChatHandler<br/>15 REST endpoints"]
        WSH["WS Handler<br/>GET /api/v1/ws?token="]
        HUB["Hub<br/>connections map[userID]→[]*Conn<br/>register · unregister · broadcast"]
        REPO["ChatRepository<br/>Raw SQL · Transactions"]

        MW --> CH
        WSH --> HUB
        CH --> REPO
        CH -.->|Push Event| HUB
    end

    subgraph DB["Postgres (Fly.io)"]
        direction LR
        T1[chats]
        T2[chat_participants]
        T3[messages]
        T4[requests]
        T5[attachments]
    end

    subgraph Storage["File Storage"]
        UPL["/data/uploads/chats/{chatId}/"]
    end

    API -->|HTTPS REST| MW
    WSM <-->|WSS| WSH
    HUB -.->|JSON Event| WSM
    REPO --> DB
    CH -->|Multipart Upload| UPL
```

---

## 2. Sequence: Create Chat + Send Message

```mermaid
sequenceDiagram
    actor Driver as Driver (iOS)
    participant API as APIClient
    participant BE as ChatHandler
    participant Repo as ChatRepository
    participant DB as Postgres
    participant Hub as WS Hub
    participant WSM as WebSocketManager
    actor Owner as Owner (iOS)

    Note over Driver: Taps "Message" on ListingDetailView

    %% Create Chat
    Driver->>API: findOrCreateChat(car_id, driver_id, owner_id)
    API->>BE: POST /api/v1/chats
    BE->>BE: Verify caller is driver or owner
    BE->>Repo: FindOrCreateChat()
    Repo->>DB: BEGIN TX
    Repo->>DB: INSERT INTO chats ... ON CONFLICT DO NOTHING
    Repo->>DB: INSERT INTO chat_participants (driver)
    Repo->>DB: INSERT INTO chat_participants (owner)
    Repo->>DB: COMMIT
    DB-->>Repo: Chat row
    Repo-->>BE: Chat
    BE-->>API: 200 {id, car_id, driver_id, owner_id}
    API-->>Driver: ChatAPIResponse
    Note over Driver: Navigate to ChatView

    %% Send Message (Optimistic)
    Driver->>Driver: Generate clientMessageId (UUID)
    Driver->>Driver: Insert local message (status: .sending)
    Driver->>API: sendMessage(chatId, body, clientMessageId)
    API->>BE: POST /api/v1/chats/{chatId}/messages
    BE->>BE: requireParticipant()
    BE->>Repo: CreateMessage()
    Repo->>DB: BEGIN TX
    Repo->>DB: INSERT INTO messages
    Repo->>DB: UPDATE chats SET last_message_at
    Repo->>DB: COMMIT
    DB-->>Repo: Message row
    Repo-->>BE: MessageResponse

    par WebSocket broadcast
        BE->>Hub: Event{type: "new_message", targets: [owner_id]}
        Hub->>WSM: JSON → Owner's connections
        WSM->>Owner: newMessagePublisher.send()
        Owner->>Owner: Deduplicate by id/clientMessageId
        Owner->>Owner: Append to messages array
    and REST response
        BE-->>API: 200 MessageResponse
        API-->>Driver: Server message (real id)
        Driver->>Driver: Replace optimistic → server message
    end

    Note over Owner: Unread badge increments on ChatsListView
```

---

## 3. Sequence: Create Request + Accept/Decline

```mermaid
sequenceDiagram
    actor Driver as Driver (iOS)
    participant API_D as APIClient
    participant BE as ChatHandler
    participant Repo as ChatRepository
    participant DB as Postgres
    participant Hub as WS Hub
    participant WSM as WebSocketManager
    actor Owner as Owner (iOS)

    Note over Driver: Opens RequestComposerSheet<br/>Fills: type, title, amount, description

    %% Create Request
    Driver->>API_D: createChatRequest(chatId, type, title, amount, ...)
    API_D->>BE: POST /api/v1/chats/{chatId}/requests
    BE->>BE: requireParticipant()
    BE->>BE: Determine target = other participant (Owner)
    BE->>BE: Default expiry = now + 48h if not set
    BE->>Repo: CreateRequest()
    Repo->>DB: BEGIN TX
    Repo->>DB: INSERT INTO requests (status='pending')
    Repo->>DB: UPDATE chats SET last_request_at
    Repo->>DB: INSERT INTO messages (type='system', body='New request: {title}')
    Repo->>DB: UPDATE chats SET last_message_at
    Repo->>DB: COMMIT
    DB-->>Repo: Request row
    Repo-->>BE: RequestResponse

    par WS to Owner
        BE->>Hub: Event{type: "request_created", targets: [owner_id]}
        Hub->>WSM: JSON → Owner's connections
        WSM->>Owner: requestCreatedPublisher.send()
        Owner->>Owner: Insert request in Requests tab
        Owner->>Owner: System message appears in Messages tab
    and REST to Driver
        BE-->>API_D: 200 RequestResponse
        API_D-->>Driver: ChatRequest (status: pending)
        Driver->>Driver: loadRequests() on sheet dismiss
    end

    Note over Owner: Sees RequestCardView with countdown timer<br/>Accept / Decline buttons shown (Owner is target_user)

    %% --- Accept Flow ---
    rect rgb(230, 245, 230)
        Note right of Owner: Accept Flow
        Owner->>BE: POST /chats/{chatId}/requests/{reqId}/respond {action: "accept"}
        BE->>Repo: RespondToRequest()
        Repo->>DB: BEGIN TX
        Repo->>DB: SELECT ... FOR UPDATE (lock request row)
        Repo->>DB: Validate: status=pending, not expired
        Repo->>DB: Validate: responder = target_user_id
        Repo->>DB: UPDATE requests SET status='accepted', resolved_at=now
        Repo->>DB: INSERT INTO messages (type='system', body='Request "{title}" was accepted')
        Repo->>DB: UPDATE chats SET last_message_at
        Repo->>DB: COMMIT

        par WS to Driver
            BE->>Hub: Event{type: "request_updated", targets: [driver_id]}
            Hub->>WSM: JSON → Driver's connections
            WSM->>Driver: requestUpdatedPublisher.send()
            Driver->>Driver: Update request status in array
        and REST to Owner
            BE-->>Owner: 200 Updated RequestResponse
            Owner->>Owner: Update request card (shows "Accepted")
        end
    end

    %% --- Decline Flow (alternative) ---
    rect rgb(245, 230, 230)
        Note right of Owner: Decline Flow (alternative)
        Owner->>BE: POST /respond {action: "decline", note: "..."}
        BE->>Repo: Same validation, status→'declined'
        Repo->>DB: UPDATE + system message "was declined: {note}"
        BE->>Hub: Event{type: "request_updated", targets: [driver_id]}
        BE-->>Owner: 200 Updated RequestResponse
    end
```

---

## 4. Request State Machine

```mermaid
stateDiagram-v2
    [*] --> pending: POST /requests<br/>(creator submits)

    pending --> accepted: target_user responds<br/>action="accept"
    pending --> declined: target_user responds<br/>action="decline"
    pending --> cancelled: creator responds<br/>action="cancel"
    pending --> expired: expires_at <= now()<br/>(lazy check on read)

    accepted --> [*]
    declined --> [*]
    cancelled --> [*]
    expired --> [*]

    note right of pending
        Row locked with SELECT FOR UPDATE
        Expiry checked before any action
        System message on every transition
    end note
```

---

## 5. Data Model

```mermaid
erDiagram
    users {
        uuid id PK
        string first_name
        string last_name
        string role "driver | car_owner"
    }

    cars {
        uuid id PK
        uuid owner_id FK
        string make
        string model
        int year
    }

    chats {
        uuid id PK
        uuid car_id FK
        uuid driver_id FK
        uuid owner_id FK
        timestamptz last_message_at
        timestamptz last_request_at
        timestamptz created_at
    }

    chat_participants {
        uuid id PK
        uuid chat_id FK
        uuid user_id FK
        timestamptz last_read_at "default epoch"
        bool auto_translate "default false"
        bool notifications_muted "default false"
        bool is_archived "default false"
    }

    messages {
        uuid id PK
        uuid chat_id FK
        uuid sender_id FK
        enum type "text | system"
        text body
        uuid client_message_id UK "nullable, dedup"
        uuid request_id FK "nullable"
        timestamptz created_at
    }

    requests {
        uuid id PK
        uuid chat_id FK
        enum type "manual_payment | delayed_payment | mechanic_service | additional_fee | generic"
        enum status "pending | accepted | declined | expired | cancelled"
        uuid created_by_id FK
        uuid target_user_id FK
        varchar title
        text description
        decimal amount "nullable"
        varchar currency "default USD"
        jsonb payload_json
        timestamptz expires_at
        timestamptz resolved_at "nullable"
    }

    attachments {
        uuid id PK
        uuid chat_id FK
        uuid message_id FK "nullable"
        uuid request_id FK "nullable"
        uuid uploader_id FK
        enum kind "image | document | video"
        varchar filename
        varchar file_path
        varchar file_url
    }

    users ||--o{ chats : "driver_id"
    users ||--o{ chats : "owner_id"
    cars ||--o{ chats : "car_id"
    chats ||--|| chat_participants : "driver entry"
    chats ||--|| chat_participants : "owner entry"
    chats ||--o{ messages : "chat_id"
    chats ||--o{ requests : "chat_id"
    chats ||--o{ attachments : "chat_id"
    requests ||--o{ messages : "request_id"
    messages ||--o{ attachments : "message_id"
    requests ||--o{ attachments : "request_id"
    users ||--o{ messages : "sender_id"
    users ||--o{ requests : "created_by_id → target_user_id"
```

---

## 6. API → Table Mapping

```mermaid
graph LR
    subgraph REST["REST Endpoints"]
        E1["POST /chats"]
        E2["GET /chats"]
        E3["POST /{chatId}/messages"]
        E4["GET /{chatId}/messages"]
        E5["POST /{chatId}/read"]
        E6["POST /{chatId}/requests"]
        E7["POST /{reqId}/respond"]
        E8["GET /{chatId}/details"]
        E9["PATCH /{chatId}/settings"]
        E10["POST /{chatId}/attachments"]
    end

    subgraph Tables["Postgres Tables"]
        TC[chats]
        TCP[chat_participants]
        TM[messages]
        TR[requests]
        TA[attachments]
    end

    E1 -->|INSERT| TC
    E1 -->|INSERT| TCP
    E2 -->|SELECT + unread count| TC
    E2 -->|JOIN| TCP
    E2 -->|COUNT subquery| TM
    E3 -->|INSERT| TM
    E3 -->|UPDATE last_message_at| TC
    E4 -->|SELECT cursor paginated| TM
    E5 -->|UPDATE last_read_at| TCP
    E6 -->|INSERT| TR
    E6 -->|INSERT system msg| TM
    E6 -->|UPDATE last_request_at| TC
    E7 -->|UPDATE status FOR UPDATE| TR
    E7 -->|INSERT system msg| TM
    E8 -->|SELECT| TC
    E8 -->|SELECT| TCP
    E8 -->|COUNT| TA
    E9 -->|UPDATE| TCP
    E10 -->|INSERT| TA
```

---

## 7. Real-Time vs REST Fallback

```mermaid
graph TB
    subgraph RT["Real-Time Path (WebSocket)"]
        direction LR
        WS1["Handler pushes Event to Hub"]
        WS2["Hub routes to target user connections"]
        WS3["WebSocketManager receives JSON"]
        WS4["PassthroughSubject publishes"]
        WS5["ViewModel updates UI"]
        WS1 --> WS2 --> WS3 --> WS4 --> WS5
    end

    subgraph FB["Fallback Path (Polling)"]
        direction LR
        FB1["WebSocket fails after 10 retries"]
        FB2["Timer fires every 5s"]
        FB3["ChatsListViewModel.fetchChats()"]
        FB4["GET /api/v1/chats"]
        FB5["Full list refresh"]
        FB1 --> FB2 --> FB3 --> FB4 --> FB5
    end

    subgraph Events["Event Types"]
        EV1["new_message → newMessagePublisher"]
        EV2["request_created → requestCreatedPublisher"]
        EV3["request_updated → requestUpdatedPublisher"]
    end

    RT -.-> Events
    FB -.->|"No granular events<br/>full list only"| Events

    style RT fill:#e8f5e9,stroke:#4caf50
    style FB fill:#fff3e0,stroke:#ff9800
```

---

## Legend

| Symbol | Meaning |
|--------|---------|
| Solid arrow `──>` | Synchronous call / direct dependency |
| Dashed arrow `-.->` | Asynchronous event / push notification |
| `par ... and ...` | Parallel execution (WS broadcast + REST response happen simultaneously) |
| `rect rgb(...)` | Highlighted alternative flow |
| `FK` | Foreign key reference |
| `UK` | Unique constraint (conditional: `WHERE NOT NULL`) |
| `PK` | Primary key |
| Blue subgraph | iOS app layer |
| Green subgraph | Backend server layer |
| Yellow/DB subgraph | Persistence layer |

### Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Deterministic chat identity** `UNIQUE(car_id, driver_id, owner_id)` | One chat per car-driver-owner triple; `INSERT ON CONFLICT DO NOTHING` makes FindOrCreate idempotent |
| **Unread = `COUNT(messages WHERE created_at > last_read_at AND sender != me)`** | No separate counter to maintain; always consistent; resets via `UPDATE last_read_at = NOW()` |
| **`client_message_id` for dedup** | Optimistic send on iOS generates UUID before POST; server enforces `UNIQUE WHERE NOT NULL`; prevents double-send on retry |
| **System messages on request transitions** | Every state change (create/accept/decline/cancel/expire) inserts a `type='system'` message; gives full audit trail in message history |
| **WS targets exclude sender** | Sender already has optimistic/REST confirmation; only other participants get push events |
| **Polling fallback after 10 WS retries** | Exponential backoff (1s base) → 5s polling interval; ensures eventual consistency without WS |
| **`SELECT FOR UPDATE` on request respond** | Row-level lock prevents race conditions on concurrent accept/decline |
