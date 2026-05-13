ALTER TABLE conversations
    DROP CONSTRAINT IF EXISTS conversations_task_options_object,
    DROP COLUMN IF EXISTS task_options;
