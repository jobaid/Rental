import { Link } from 'react-router-dom';

export default function Landing() {
  return (
    <>
      <section className="hero">
        <h1>Rent smarter.</h1>
        <p>
          A complete fleet &amp; vehicle rental platform — bookings, contracts, payments,
          maintenance and reporting in one place. For rental companies and the people they serve.
        </p>
        <div style={{ display: 'flex', gap: 12, justifyContent: 'center', flexWrap: 'wrap' }}>
          <Link to="/search" className="btn">Find a vehicle</Link>
          <Link to="/login" className="btn secondary" style={{ padding: '10px 16px' }}>Operator sign in</Link>
        </div>
      </section>

      <section style={{ maxWidth: 1100, margin: '0 auto', padding: 32 }}>
        <div className="row" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))' }}>
          <div className="card">
            <h3>Live availability</h3>
            <p className="muted">Pick up and return across every branch, with real-time inventory.</p>
          </div>
          <div className="card">
            <h3>Transparent pricing</h3>
            <p className="muted">Daily, weekly and monthly rates with the deposit shown up front.</p>
          </div>
          <div className="card">
            <h3>Secure booking</h3>
            <p className="muted">Sign your rental agreement online. Pay deposits through verified gateways.</p>
          </div>
        </div>
      </section>
    </>
  );
}
