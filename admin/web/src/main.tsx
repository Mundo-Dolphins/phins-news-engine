import { render } from "preact";
import { LocationProvider, Router, Route } from "preact-iso";
import { Layout } from "./components/Layout";
import { Login } from "./routes/Login";
import { Dashboard } from "./routes/Dashboard";
import { Catalog } from "./routes/Catalog";
import { ActionPage } from "./routes/ActionPage";
import { Jobs } from "./routes/Jobs";
import { JobDetail } from "./routes/JobDetail";
import { Audit } from "./routes/Audit";
import { Review } from "./routes/Review";
import { ReviewTrain } from "./routes/ReviewTrain";
import { ReviewDataset } from "./routes/ReviewDataset";
import { ReviewEntities } from "./routes/ReviewEntities";
import { ReviewEvents } from "./routes/ReviewEvents";
import { ReviewDictionary } from "./routes/ReviewDictionary";
import { ReviewArticles } from "./routes/ReviewArticles";
import { ReviewTerms } from "./routes/ReviewTerms";
import { Cycle } from "./routes/Cycle";
import "./style.css";

function NotFound() {
  return <p>Page not found.</p>;
}

function App() {
  const onLogin = location.pathname === "/login";
  const routes = (
    <Router>
      <Route path="/login" component={Login} />
      <Route path="/" component={Dashboard} />
      <Route path="/actions" component={Catalog} />
      <Route path="/actions/:name" component={ActionPage} />
      <Route path="/jobs" component={Jobs} />
      <Route path="/jobs/:id" component={JobDetail} />
      <Route path="/audit" component={Audit} />
      <Route path="/cycle" component={Cycle} />
      <Route path="/review" component={Review} />
      <Route path="/review/train" component={ReviewTrain} />
      <Route path="/review/dataset" component={ReviewDataset} />
      <Route path="/review/entities" component={ReviewEntities} />
      <Route path="/review/events" component={ReviewEvents} />
      <Route path="/review/dictionary" component={ReviewDictionary} />
      <Route path="/review/articles" component={ReviewArticles} />
      <Route path="/review/terms" component={ReviewTerms} />
      <Route default component={NotFound} />
    </Router>
  );
  return <LocationProvider>{onLogin ? routes : <Layout>{routes}</Layout>}</LocationProvider>;
}

render(<App />, document.getElementById("app")!);
