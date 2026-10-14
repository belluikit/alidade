<script lang="ts">
  import { helper } from './util';

  function label() {
    return helper(1);
  }

  function badge() {
    const inner = () => helper(3);
    return inner();
  }
</script>

<p>{label()}</p>
<button onclick={() => helper(2)}>x</button>
