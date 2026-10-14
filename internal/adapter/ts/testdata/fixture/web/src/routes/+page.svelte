<script lang="ts">
  import { newUser } from "$lib/api";
  import { Role } from "$gen/app/v1/user_pb.js";
  import Card from "$lib/Card.svelte";
  import "../app.css";

  const user = newUser("a@b.c");
  const admin = user.role === Role.ADMIN;
</script>

<Card {user} />
{#if admin}<p>admin</p>{/if}
