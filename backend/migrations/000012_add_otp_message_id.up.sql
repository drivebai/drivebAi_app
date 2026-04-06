-- Add MailerSend message_id to login_otps for delivery tracking
ALTER TABLE login_otps ADD COLUMN message_id VARCHAR(255);
CREATE INDEX idx_login_otps_message_id ON login_otps(message_id) WHERE message_id IS NOT NULL;
