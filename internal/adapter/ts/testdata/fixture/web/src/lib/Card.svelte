<script lang="ts">
  import type { User } from "$gen/app/v1/user_pb.js";
  let { user }: { user: User } = $props();
</script>

<div class="card">{user.email}</div>
