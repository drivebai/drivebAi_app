ALTER TABLE messages
    ADD COLUMN sender_kind VARCHAR(10) NOT NULL DEFAULT 'user'
        CHECK (sender_kind IN ('user', 'admin'));
