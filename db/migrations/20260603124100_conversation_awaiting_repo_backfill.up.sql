UPDATE conversations
   SET awaiting_repo = TRUE
 WHERE sandbox_id = ''
   AND github_owner = ''
   AND github_repo = ''
   AND array_length(history, 1) > 0
   AND response_blocks::text ILIKE '%Which repository%';
