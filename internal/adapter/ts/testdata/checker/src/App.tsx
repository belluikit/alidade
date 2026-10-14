import mk, { Store, twice, api, type Service, type Saver } from './util';
import { help } from './index';

function Button(props: { onClick(): void }) {
  props.onClick();
  return null;
}

export function App(svc: Service) {
  const local = () => twice(1);
  local();
  svc.open('x');
  const s = new Store();
  s.add('a');
  api.load();
  help(5);
  mk();
  [1, 2].map((x) => x);
  return <Button onClick={() => {}} />;
}

App(null as unknown as Service);

export function persist(s: Saver) {
  s.save();
  s.close();
}
