UPDATE conversations
   SET awaiting_repo = FALSE
 WHERE awaiting_repo = TRUE
   AND sandbox_id = ''
   AND github_owner = ''
   AND github_repo = ''
   AND response_blocks::text ILIKE '%Which repository%';
