-- Communication Center: threads, messages, templates, notification rules, internal notes.

CREATE TABLE IF NOT EXISTS communication_threads (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    customer_id     UUID REFERENCES customers(id) ON DELETE CASCADE,
    reservation_id  UUID REFERENCES reservations(id) ON DELETE SET NULL,
    assigned_user_id UUID REFERENCES users(id),
    status          TEXT NOT NULL DEFAULT 'open'
                      CHECK (status IN ('open','resolved','archived')),
    unread_count    INT NOT NULL DEFAULT 0,
    last_message_at TIMESTAMPTZ,
    last_preview    TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, customer_id, reservation_id)
);
CREATE INDEX IF NOT EXISTS threads_company_lastmsg_idx ON communication_threads(company_id, last_message_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS threads_reservation_idx    ON communication_threads(reservation_id);

CREATE TABLE IF NOT EXISTS communication_messages (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id          UUID NOT NULL REFERENCES communication_threads(id) ON DELETE CASCADE,
    company_id         UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    customer_id        UUID REFERENCES customers(id) ON DELETE SET NULL,
    reservation_id     UUID REFERENCES reservations(id) ON DELETE SET NULL,
    sender_type        TEXT NOT NULL CHECK (sender_type IN ('staff','customer','system')),
    sender_user_id     UUID REFERENCES users(id),
    channel            TEXT NOT NULL CHECK (channel IN ('email','sms','note')),
    direction          TEXT NOT NULL CHECK (direction IN ('outbound','inbound','internal')),
    recipient          TEXT,
    subject            TEXT,
    body               TEXT NOT NULL,
    provider_message_id TEXT,
    status             TEXT NOT NULL DEFAULT 'queued'
                         CHECK (status IN ('queued','sending','sent','delivered','failed','read')),
    error_message      TEXT,
    idempotency_key    TEXT,
    sent_at            TIMESTAMPTZ,
    delivered_at       TIMESTAMPTZ,
    read_at            TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS messages_thread_idx ON communication_messages(thread_id, created_at DESC);
CREATE INDEX IF NOT EXISTS messages_company_channel_idx ON communication_messages(company_id, channel, created_at DESC);

CREATE TABLE IF NOT EXISTS communication_attachments (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id     UUID NOT NULL REFERENCES communication_messages(id) ON DELETE CASCADE,
    original_filename TEXT NOT NULL,
    storage_key    TEXT NOT NULL,
    mime_type      TEXT NOT NULL,
    file_size      BIGINT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS communication_templates (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id  UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    code        TEXT NOT NULL,
    name        TEXT NOT NULL,
    channel     TEXT NOT NULL CHECK (channel IN ('email','sms')),
    subject     TEXT,
    body        TEXT NOT NULL,
    is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    created_by  UUID REFERENCES users(id),
    updated_by  UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, code)
);

CREATE TABLE IF NOT EXISTS communication_preferences (
    customer_id                UUID PRIMARY KEY REFERENCES customers(id) ON DELETE CASCADE,
    email_transactional        BOOLEAN NOT NULL DEFAULT TRUE,
    sms_transactional          BOOLEAN NOT NULL DEFAULT TRUE,
    marketing_email            BOOLEAN NOT NULL DEFAULT FALSE,
    marketing_sms              BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS communication_notification_rules (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id    UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    event_type    TEXT NOT NULL,
    channel       TEXT NOT NULL CHECK (channel IN ('email','sms','both')),
    template_code TEXT,
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    delay_minutes INT NOT NULL DEFAULT 0,
    staff_also    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, event_type, channel)
);

CREATE TABLE IF NOT EXISTS communication_internal_notes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id       UUID NOT NULL REFERENCES communication_threads(id) ON DELETE CASCADE,
    author_user_id  UUID NOT NULL REFERENCES users(id),
    note            TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS notes_thread_idx ON communication_internal_notes(thread_id, created_at DESC);

-- Seed 14 default templates per company.
DO $$
DECLARE c RECORD;
BEGIN
  FOR c IN SELECT id FROM companies LOOP
    INSERT INTO communication_templates (company_id, code, name, channel, subject, body) VALUES
      (c.id, 'booking_confirmation', 'Booking confirmation', 'email',
        'Your booking {{reservation_number}} is confirmed',
        E'Hi {{customer_name}},\n\nYour booking is confirmed.\n\nReservation: {{reservation_number}}\nVehicle: {{vehicle_name}}\nPickup: {{pickup_date}} {{pickup_time}} at {{pickup_location}}\nReturn: {{return_date}} {{return_time}} at {{return_location}}\n\nTotal: {{reservation_total}}\nPaid: {{amount_paid}}\nBalance: {{outstanding_balance}}\n\nThanks,\n{{company_name}}'),
      (c.id, 'quote_sent', 'Quote sent', 'email',
        'Your quote from {{company_name}}',
        E'Hi {{customer_name}},\n\nHere is the quote for {{vehicle_name}} from {{pickup_date}} to {{return_date}}.\nTotal: {{reservation_total}}\n\nReply to this email to confirm.\n\n{{company_name}}'),
      (c.id, 'quote_follow_up', 'Quote follow-up', 'email',
        'Still thinking it over?',
        E'Hi {{customer_name}},\n\nJust checking in on the quote we sent for {{vehicle_name}}. Let me know if you have questions.\n\n{{company_name}}'),
      (c.id, 'reservation_modified', 'Reservation modified', 'email',
        'Changes to your booking {{reservation_number}}',
        E'Hi {{customer_name}},\n\nYour booking {{reservation_number}} has been updated.\n\nNew pickup: {{pickup_date}} {{pickup_time}}\nNew return:  {{return_date}} {{return_time}}\n\nTotal: {{reservation_total}}\n\n{{company_name}}'),
      (c.id, 'pickup_reminder', 'Upcoming pickup reminder', 'email',
        'Pickup tomorrow — {{reservation_number}}',
        E'Hi {{customer_name}},\n\nReminder: your {{vehicle_name}} is ready for pickup on {{pickup_date}} at {{pickup_time}}.\nPickup location: {{pickup_location}}.\n\nBring your drivers license and the card on file.\n\n{{company_name}}'),
      (c.id, 'pickup_instructions', 'Pickup instructions', 'email',
        'How to collect your rental',
        E'Hi {{customer_name}},\n\nHere are the pickup instructions for {{reservation_number}}:\n\n1. Arrive at {{pickup_location}} on {{pickup_date}} at {{pickup_time}}.\n2. Bring your drivers license.\n3. Our team will complete the pre-rental inspection with you.\n\nSee you soon,\n{{company_name}}'),
      (c.id, 'return_reminder', 'Return reminder', 'email',
        'Return reminder — {{reservation_number}}',
        E'Hi {{customer_name}},\n\nFriendly reminder: please return {{vehicle_name}} on {{return_date}} at {{return_time}} to {{return_location}}.\n\nSafe travels,\n{{company_name}}'),
      (c.id, 'overdue_return', 'Overdue return', 'email',
        'Vehicle return is overdue — {{reservation_number}}',
        E'Hi {{customer_name}},\n\nOur records show that your rental {{reservation_number}} is overdue. Please contact us as soon as possible on {{company_phone}}.\n\n{{company_name}}'),
      (c.id, 'payment_reminder', 'Outstanding payment reminder', 'email',
        'Balance due on {{reservation_number}}',
        E'Hi {{customer_name}},\n\nYou have an outstanding balance of {{outstanding_balance}} on booking {{reservation_number}}.\n\nPlease let us know how you''d like to settle this.\n\n{{company_name}}'),
      (c.id, 'deposit_info', 'Security deposit info', 'email',
        'Security deposit — {{reservation_number}}',
        E'Hi {{customer_name}},\n\nA refundable security deposit of {{security_deposit}} will be held at pickup for {{reservation_number}}.\n\n{{company_name}}'),
      (c.id, 'agreement_notification', 'Rental agreement ready', 'email',
        'Please sign your rental agreement',
        E'Hi {{customer_name}},\n\nYour rental agreement for {{reservation_number}} is ready to sign. Please follow the link in your portal.\n\n{{company_name}}'),
      (c.id, 'inspection_notification', 'Inspection report ready', 'email',
        'Vehicle inspection report — {{reservation_number}}',
        E'Hi {{customer_name}},\n\nThe inspection report for your rental {{reservation_number}} is available in your portal.\n\n{{company_name}}'),
      (c.id, 'cancellation', 'Reservation cancelled', 'email',
        'Your booking {{reservation_number}} has been cancelled',
        E'Hi {{customer_name}},\n\nYour booking {{reservation_number}} has been cancelled.\n\nIf this was unexpected, please contact us on {{company_phone}}.\n\n{{company_name}}'),
      (c.id, 'thank_you', 'Thank-you after return', 'email',
        'Thanks for renting with {{company_name}}',
        E'Hi {{customer_name}},\n\nThanks for returning {{vehicle_name}} on time. We hope to see you again!\n\n{{company_name}}'),
      -- SMS defaults (short)
      (c.id, 'sms_pickup_reminder', 'Pickup reminder (SMS)', 'sms', NULL,
        '{{company_name}}: Reminder — pickup {{vehicle_name}} on {{pickup_date}} at {{pickup_time}}. Reservation {{reservation_number}}.'),
      (c.id, 'sms_return_reminder', 'Return reminder (SMS)', 'sms', NULL,
        '{{company_name}}: Please return {{vehicle_name}} by {{return_date}} {{return_time}}. Reservation {{reservation_number}}.'),
      (c.id, 'sms_overdue', 'Overdue (SMS)', 'sms', NULL,
        '{{company_name}}: Your rental {{reservation_number}} is overdue. Please call {{company_phone}}.')
    ON CONFLICT (company_id, code) DO NOTHING;
  END LOOP;
END $$;

-- Seed default notification rules.
DO $$
DECLARE c RECORD;
BEGIN
  FOR c IN SELECT id FROM companies LOOP
    INSERT INTO communication_notification_rules (company_id, event_type, channel, template_code, enabled, delay_minutes) VALUES
      (c.id, 'booking_confirmed', 'email', 'booking_confirmation', TRUE, 0),
      (c.id, 'pickup_tomorrow',   'email', 'pickup_reminder',      TRUE, 0),
      (c.id, 'return_tomorrow',   'email', 'return_reminder',      TRUE, 0),
      (c.id, 'overdue_return',    'email', 'overdue_return',       TRUE, 0),
      (c.id, 'payment_due',       'email', 'payment_reminder',     TRUE, 0),
      (c.id, 'cancelled',         'email', 'cancellation',         TRUE, 0),
      (c.id, 'inspection_ready',  'email', 'inspection_notification', TRUE, 0),
      (c.id, 'rental_completed',  'email', 'thank_you',            TRUE, 60)
    ON CONFLICT (company_id, event_type, channel) DO NOTHING;
  END LOOP;
END $$;
