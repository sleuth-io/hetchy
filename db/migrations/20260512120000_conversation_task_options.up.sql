ALTER TABLE conversations
    ADD COLUMN task_options JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD CONSTRAINT conversations_task_options_object
        CHECK (jsonb_typeof(task_options) = 'object');
