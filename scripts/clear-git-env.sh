# Source only when commands select a repository independently of the parent Git
# operation, such as the test suite launched by pre-push. Never source this from
# pre-commit: that hook needs its parent's GIT_INDEX_FILE for partial commits.
for git_env_name in $(env | sed -n 's/^\(GIT_[A-Za-z0-9_]*\)=.*/\1/p'); do
  unset "$git_env_name"
done
unset git_env_name
